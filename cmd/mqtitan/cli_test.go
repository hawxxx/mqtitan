package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/thresholds"
)

func TestQuickRejectsWrappedQoS(t *testing.T) {
	if err := quick([]string{"--qos", "256", "--duration", "1ms"}); err == nil {
		t.Fatal("qos 256 wrapped into valid qos")
	}
}
func TestQuickRejectsUnknownArguments(t *testing.T) {
	if err := quick([]string{"--duration", "1ms", "unexpected"}); err == nil {
		t.Fatal("silently ignored positional arguments")
	}
}
func TestRemoteListenRequiresAuthentication(t *testing.T) {
	if err := checkListen("0.0.0.0:8080", "", false); err == nil {
		t.Fatal("remote control exposed without authentication")
	}
	if err := checkListen("127.0.0.1:8080", "", false); err != nil {
		t.Fatal(err)
	}
}
func TestSplitWorkersTrimsSpacesAndEmptyEntries(t *testing.T) {
	if got := splitWorkers(" worker-a, worker-b ,,"); !reflect.DeepEqual(got, []string{"worker-a", "worker-b"}) {
		t.Fatalf("got %q", got)
	}
	if got := splitWorkers(""); got != nil {
		t.Fatalf("got %q", got)
	}
}
func TestRunAcceptsFlagsBeforeScenario(t *testing.T) {
	err := runScenario([]string{"--controller", "http://127.0.0.1:1", "missing.yaml"})
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("scenario path not taken after flags: %v", err)
	}
	if err := runScenario(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing usage: %v", err)
	}
}
func TestRequestReportsControllerErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_SCENARIO","message":"clients.count must be positive"}}`))
	}))
	defer srv.Close()
	err := request(context.Background(), srv.URL, "POST", "/api/v1/tests", map[string]string{}, nil)
	if err == nil || err.Error() != "controller returned HTTP 400: clients.count must be positive" {
		t.Fatalf("got %v", err)
	}
}
func TestInterruptDuringPollStopsRemoteTest(t *testing.T) {
	polled := make(chan struct{}, 1)
	var stopped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/tests":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"data":{"id":"t1","status":"running"}}`))
		case r.Method == "GET":
			select {
			case polled <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		case r.URL.Path == "/api/v1/tests/t1/stop":
			stopped.Store(true)
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"data":{"status":"stopping"}}`))
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-polled:
		case <-time.After(5 * time.Second):
		}
		cancel()
	}()
	err := remoteRun(ctx, srv.URL, "scenario", nil)
	var exit exitError
	if !errors.As(err, &exit) || exit.code != 130 {
		t.Fatalf("got %v", err)
	}
	if !stopped.Load() {
		t.Fatal("interrupted run left the remote test running")
	}
}

func TestRequestReportsTransportCause(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	err = request(context.Background(), "http://"+address, "GET", "/api/v1/tests", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "controller request failed: ") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error = %v, want transport cause", err)
	}
}

const cliScenario = `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: cli
broker: {url: 'mqtts://broker.example.com:8883', version: '3.1.1', keepAlive: 180s, idleTimeoutHint: 100s, dns: {resolveEachDial: true, resolvers: ['192.0.2.53:53', '192.0.2.54:53'], pick: random, family: ipv4}, addressLabels: [{cidr: 192.0.2.0/25, label: a}], tls: {hostnameVerification: cn-fallback}}
clients: {count: 10000, idTemplate: 'c-${sequence:08}'}
stages: [{duration: 1s, targetClients: 10000}]
workloads: [{name: idle, type: connection, clients: 10000}]
safety: {forbiddenTopicPrefixes: ['s1/', 's2/'], clientIdDenyRegex: '^[0-9a-f]{12}$'}
`

func TestPlanPrintsSafetyDNSAndWarnings(t *testing.T) {
	s, err := scenario.Parse([]byte(cliScenario))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printPlan(&out, s, s.Plan())
	for _, want := range []string{
		"OK    safety  2 forbidden prefixes, client ID rule checked on 10000 IDs",
		"dns: resolveEachDial, 2 resolvers, pick random, ipv4",
		"1 address label",
		"hostname verification: cn-fallback",
		"broker.idleTimeoutHint",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	s.Safety = scenario.Safety{}
	s.Broker.DNS, s.Broker.AddressLabels = nil, nil
	out.Reset()
	printPlan(&out, s, s.Plan())
	if !strings.Contains(out.String(), "WARN  safety  no forbiddenTopicPrefixes set") || strings.Contains(out.String(), "dns:") {
		t.Errorf("unexpected:\n%s", out.String())
	}
}

func TestValidatePrintsWarningsBeforeValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	if err := os.WriteFile(path, []byte(cliScenario), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{path}} {
		var out bytes.Buffer
		if err := validateCommand(&out, args); err != nil {
			t.Fatal(err)
		}
		warn, valid := strings.Index(out.String(), "WARN"), strings.Index(out.String(), "VALID cli")
		if warn < 0 || valid < warn {
			t.Fatalf("warning must precede VALID:\n%s", out.String())
		}
	}
}

func TestValidateStrictSecretsFailsOnWideKey(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "client.key")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	y := strings.Replace(cliScenario, "tls: {hostnameVerification: cn-fallback}", "tls: {certFile: "+key+", keyFile: "+key+"}", 1)
	y = strings.Replace(y, "safety: {", "safety: {strictSecrets: true, ", 1)
	path := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := validateCommand(&out, []string{"--", path})
	if err == nil || err.Error() != "refusing key file client.key: permissions 0644 are wider than 0600 (safety.strictSecrets)" || !strings.Contains(out.String(), "WARN  secrets  client.key: permissions 0644") || strings.Contains(out.String(), dir) {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

func TestPlanCommandFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.yaml")
	y := strings.Replace(strings.Replace(cliScenario, "count: 10000", "count: 60000", 1), "targetClients: 10000", "targetClients: 60000", 1)
	y = strings.Replace(y, "clients: 10000}]", "clients: 60000}]", 1)
	y = strings.Replace(y, ", addressLabels: [{cidr: 192.0.2.0/25, label: a}]", "", 1)
	if err := os.WriteFile(path, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	var one, two bytes.Buffer
	if err := planCommand(&one, []string{path}); err != nil {
		t.Fatal(err)
	}
	if err := planCommand(&two, []string{"--workers", "3", path}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(one.String(), "source ports") || strings.Contains(two.String(), "source ports") {
		t.Fatalf("workers flag ignored:\n%s\n---\n%s", one.String(), two.String())
	}
}

func TestValidateStrictScopeIsKeyFileOnly(t *testing.T) {
	root := t.TempDir()
	_ = os.Mkdir(filepath.Join(root, ".git"), 0o700)
	ca := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(ca, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	key := filepath.Join(other, "client.key")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	write := func(tls string) string {
		y := strings.Replace(cliScenario, "tls: {hostnameVerification: cn-fallback}", "tls: {"+tls+"}", 1)
		y = strings.Replace(y, "safety: {", "safety: {strictSecrets: true, ", 1)
		p := filepath.Join(t.TempDir(), "s.yaml")
		if err := os.WriteFile(p, []byte(y), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	var out bytes.Buffer
	if err := validateCommand(&out, []string{write("caFile: " + ca)}); err != nil || !strings.Contains(out.String(), "WARN  secrets  ca.pem: file is inside a git work tree") || !strings.Contains(out.String(), "VALID cli") {
		t.Fatalf("ca: err=%v out=%s", err, out.String())
	}
	gitKey := filepath.Join(root, "client.key")
	if err := os.WriteFile(gitKey, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := validateCommand(&out, []string{write("certFile: " + gitKey + ", keyFile: " + gitKey)})
	if err == nil || !strings.HasPrefix(err.Error(), "refusing key file client.key: file is inside a git work tree") || !strings.HasSuffix(err.Error(), "(safety.strictSecrets)") {
		t.Fatalf("key: %v", err)
	}
}

func TestExamplesValidate(t *testing.T) {
	files, err := filepath.Glob("../../examples/*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range files {
		if filepath.Base(f) == "nlb-balance.yaml" {
			found = true
		}
		s, err := scenario.Load(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if err := thresholds.Validate(s.Thresholds); err != nil {
			t.Errorf("%s: thresholds: %v", f, err)
		}
	}
	if !found {
		t.Fatalf("examples/nlb-balance.yaml missing among %d examples", len(files))
	}
}
