package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/api"
	"github.com/mqtitan/mqtitan/internal/authn"
	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
	"github.com/mqtitan/mqtitan/internal/thresholds"
	"github.com/mqtitan/mqtitan/internal/worker"
	"github.com/mqtitan/mqtitan/web"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type runOptions struct {
	Listen, Data, Controller, Workers, Web string
	AllowRemote                            bool
}

func runFlags(f *flag.FlagSet) *runOptions {
	o := &runOptions{}
	f.StringVar(&o.Listen, "listen", "127.0.0.1:8080", "dashboard listen address")
	f.StringVar(&o.Data, "data", "mqtitan.db", "encrypted SQLite database")
	f.StringVar(&o.Controller, "controller", controllerDefault(""), "remote controller URL (env MQTITAN_CONTROLLER)")
	f.StringVar(&o.Workers, "workers", "", "comma-separated distributed worker IDs")
	f.StringVar(&o.Web, "web", "", "override embedded UI asset directory")
	return o
}
func checkListen(address, token string, allow bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && token == "" && !allow {
		return errors.New("remote listen requires MQTITAN_TOKEN or explicit --allow-remote-local-mode")
	}
	return nil
}

type localServer struct {
	manager *api.Manager
	store   *storage.Store
	server  *http.Server
	errors  chan error
	address string
}

func openServer(o runOptions) (*localServer, error) {
	token := os.Getenv("MQTITAN_TOKEN")
	authConfig := authn.Config{Token: token, BasicUsername: os.Getenv("MQTITAN_BASIC_USERNAME"), BasicPassword: os.Getenv("MQTITAN_BASIC_PASSWORD"), OIDCIssuer: os.Getenv("MQTITAN_OIDC_ISSUER"), OIDCClientID: os.Getenv("MQTITAN_OIDC_CLIENT_ID"), ProxyHeader: os.Getenv("MQTITAN_PROXY_IDENTITY_HEADER")}
	if cidrs := os.Getenv("MQTITAN_TRUSTED_PROXY_CIDRS"); cidrs != "" {
		authConfig.TrustedProxyCIDRs = strings.Split(cidrs, ",")
	}
	authMarker := token
	if authConfig.Enabled() {
		authMarker = "configured"
	}
	if err := checkListen(o.Listen, authMarker, o.AllowRemote); err != nil {
		return nil, err
	}
	authCtx, authCancel := context.WithTimeout(context.Background(), 10*time.Second)
	authorize, authErr := authn.New(authCtx, authConfig)
	authCancel()
	if authErr != nil {
		return nil, authErr
	}
	s, err := storage.Open(o.Data)
	if err != nil {
		return nil, err
	}
	m, err := api.NewManager(s)
	if err != nil {
		s.Close()
		return nil, err
	}
	if base := os.Getenv("MQTITAN_EMQX_URL"); base != "" {
		m.ConfigureEMQX(&emqx.Client{BaseURL: base, APIKey: os.Getenv("MQTITAN_EMQX_API_KEY"), APISecret: os.Getenv("MQTITAN_EMQX_API_SECRET")})
	}
	listener, err := net.Listen("tcp", o.Listen)
	if err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	assets, _ := web.FS()
	apiOptions := api.Options{Token: token, WebFS: assets, Authorize: authorize}
	if o.Web != "" {
		apiOptions.WebFS = nil
		apiOptions.WebDir = o.Web
	}
	handler := api.Handler(m, apiOptions)
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	l := &localServer{m, s, srv, make(chan error, 1), listener.Addr().String()}
	go func() { l.errors <- srv.Serve(listener) }()
	return l, nil
}
func (s *localServer) Close() {
	s.manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
	_ = s.store.Close()
}
func serve(args []string) error {
	f := flag.NewFlagSet("serve", flag.ContinueOnError)
	o := runFlags(f)
	f.BoolVar(&o.AllowRemote, "allow-remote-local-mode", false, "explicitly permit unauthenticated remote demo access")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	l, err := openServer(*o)
	if err != nil {
		return err
	}
	defer l.Close()
	fmt.Printf("MQTTitan controller: http://%s\n", l.address)
	ctx, cancel := signalContext()
	defer cancel()
	select {
	case <-ctx.Done():
		return nil
	case err := <-l.errors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func runScenario(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mqtitan run SCENARIO [flags]")
	}
	path := args[0]
	f := flag.NewFlagSet("run", flag.ContinueOnError)
	opts := runFlags(f)
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return executeScenario(string(raw), *opts)
}
func executeScenario(raw string, o runOptions) error {
	s, err := scenario.Parse([]byte(raw))
	if err != nil {
		return err
	}
	if err = thresholds.Validate(s.Thresholds); err != nil {
		return err
	}
	printPlan(s)
	ctx, cancel := signalContext()
	defer cancel()
	ids := []string(nil)
	if o.Workers != "" {
		ids = strings.Split(o.Workers, ",")
	}
	if o.Controller != "" {
		return remoteRun(ctx, o.Controller, raw, ids)
	}
	l, err := openServer(o)
	if err != nil {
		return err
	}
	defer l.Close()
	test, err := l.manager.StartOnWorkers(raw, ids)
	if err != nil {
		return err
	}
	fmt.Printf("Test %s\nDashboard: http://%s\n", test.ID, l.address)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last uint64
	for {
		select {
		case <-ctx.Done():
			_ = l.manager.Stop(test.ID)
			return exitError{130, "test interrupted"}
		case err := <-l.errors:
			return err
		case <-ticker.C:
			t, err := l.manager.Get(test.ID)
			if err != nil {
				return err
			}
			snap := t.Snapshot
			fmt.Printf("connected=%d/%d publish=%d/s total=%d received=%d errors=%d p95=%s\n", snap.Connected, snap.TargetClients, snap.Published-last, snap.Published, snap.Received, snap.ConnectErrors+snap.PublishErrors, snap.P95)
			last = snap.Published
			if t.Status != "running" {
				return verdict(t)
			}
		}
	}
}
func verdict(t api.Test) error {
	fmt.Printf("RESULT %s  peak=%d messages=%d p99=%s\n", strings.ToUpper(t.Status), t.Snapshot.PeakConnected, t.Snapshot.Published, t.Snapshot.P99)
	for _, r := range t.Thresholds {
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Printf("%s %-28s %s %s\n", status, r.Name, r.Observed, r.Expression)
	}
	if t.Status == "failed" {
		return exitError{2, "test failed thresholds or execution"}
	}
	if t.Status == "stopped" || t.Status == "interrupted" {
		return exitError{130, "test interrupted"}
	}
	return nil
}
func workerCommand(args []string) error {
	f := flag.NewFlagSet("worker", flag.ContinueOnError)
	controller := f.String("controller", controllerDefault("http://127.0.0.1:8080"), "controller URL (env MQTITAN_CONTROLLER)")
	id := f.String("id", "", "unique worker ID")
	health := f.String("health-listen", "127.0.0.1:8081", "health-only listen address")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		hostname, _ := os.Hostname()
		*id = hostname
	}
	ctx, cancel := signalContext()
	defer cancel()
	mux := http.NewServeMux()
	var ready atomic.Bool
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	})
	listener, err := net.Listen("tcp", *health)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()
	err = worker.Run(ctx, worker.Options{Controller: *controller, ID: *id, Token: os.Getenv("MQTITAN_TOKEN"), OnReady: ready.Store})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
