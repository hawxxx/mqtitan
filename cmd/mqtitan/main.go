package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/mqtt"
	"github.com/mqtitan/mqtitan/internal/observability"
	"github.com/mqtitan/mqtitan/internal/platform"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/thresholds"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// controllerDefault lets MQTITAN_CONTROLLER replace the per-command --controller flag.
func controllerDefault(fallback string) string {
	if v := os.Getenv("MQTITAN_CONTROLLER"); v != "" {
		return v
	}
	return fallback
}

type exitError struct {
	code    int
	message string
}

func (e exitError) Error() string { return e.message }
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	shutdown, traceErr := observability.Setup(context.Background())
	if traceErr != nil {
		fmt.Fprintln(os.Stderr, "error: tracing configuration invalid")
		os.Exit(1)
	}
	err := run(os.Args[1:])
	// os.Exit skips deferred calls, so flush spans explicitly: failed runs are
	// the ones whose traces matter most.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = shutdown(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var e exitError
		if errors.As(err, &e) {
			os.Exit(e.code)
		}
		os.Exit(1)
	}
}
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("command required")
	}
	switch args[0] {
	case "validate":
		return validateCommand(os.Stdout, args[1:])
	case "plan":
		return planCommand(os.Stdout, args[1:])
	case "run":
		return runScenario(args[1:])
	case "quick":
		return quick(args[1:])
	case "serve":
		return serve(args[1:])
	case "worker":
		return workerCommand(args[1:])
	case "status", "stop", "results", "export", "workers", "scenarios", "delete-scenario":
		return remoteCommand(args[0], args[1:])
	case "doctor":
		for _, c := range platform.Doctor() {
			fmt.Printf("%-5s %-20s %s", c.Status, c.Name, c.Value)
			if c.Recommendation != "" {
				fmt.Printf("  %s", c.Recommendation)
			}
			fmt.Println()
		}
		fmt.Println("INFO  memory              " + platform.MemoryTotal())
		return nil
	case "version", "--version", "-v":
		fmt.Println("mqtitan " + version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: mqtitan <quick|validate|plan|run|serve|worker|status|stop|results|export|workers|scenarios|delete-scenario|doctor|version>\nquick --broker mqtt://localhost:1883 --clients 10000 --rate 1\nserve --listen 127.0.0.1:8080 --data mqtitan.db\nrun scenario.yaml [--controller http://localhost:8080] [--workers worker-a,worker-b]\n\nEnvironment: MQTITAN_CONTROLLER (default --controller), MQTITAN_TOKEN (bearer token), MQTITAN_MQTT_PASSWORD")
}

func quick(args []string) error {
	f := flag.NewFlagSet("quick", flag.ContinueOnError)
	broker := f.String("broker", "mqtt://localhost:1883", "broker URL")
	clients := f.Int("clients", 100, "client count")
	var rate float64
	f.Float64Var(&rate, "rate", 1, "messages/client/second")
	f.Float64Var(&rate, "publish-rate", 1, "messages/client/second")
	topic := f.String("topic", "devices/${clientId}/telemetry", "topic template")
	qos := f.Int("qos", 0, "MQTT QoS 0/1/2")
	duration := f.Duration("duration", 30*time.Second, "test duration")
	payloadSize := f.Int("payload-size", 128, "payload bytes")
	version := f.String("mqtt-version", "3.1.1", "MQTT 3.1.1 or 5")
	username := f.String("username", "", "MQTT username; password from MQTITAN_MQTT_PASSWORD")
	save := f.String("save", "", "save scenario YAML to file")
	opts := runFlags(f)
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *qos < 0 || *qos > 2 {
		return errors.New("qos must be 0, 1, or 2")
	}
	s := scenario.Scenario{APIVersion: "mqtitan.io/v1alpha1", Kind: "Scenario", Name: "quick", Broker: scenario.Broker{URL: *broker, Version: *version, Username: *username, Password: os.Getenv("MQTITAN_MQTT_PASSWORD"), ConnectTimeout: scenario.Duration{Duration: 10 * time.Second}, KeepAlive: scenario.Duration{Duration: 30 * time.Second}, CleanStart: true}, Clients: scenario.Clients{Count: *clients, IDTemplate: "mqtitan-${sequence:08}"}, Stages: []scenario.Stage{{Duration: scenario.Duration{Duration: *duration}, TargetClients: *clients}}, Workloads: []scenario.Workload{{Name: "quick", Type: "publisher", Clients: *clients, Topic: *topic, QoS: byte(*qos), RatePerClient: rate, Payload: scenario.Payload{Type: "random", Size: *payloadSize}}}}
	if err := s.Validate(); err != nil {
		return err
	}
	raw, err := scenario.Encode(s)
	if err != nil {
		return err
	}
	if *save != "" {
		if s.Broker.Password != "" {
			return errors.New("--save with inline password refused; save without credentials")
		}
		if err = os.WriteFile(*save, raw, 0600); err != nil {
			return err
		}
	}
	return executeScenario(string(raw), *opts)
}

// parseScenarioArgs takes the scenario path first or last, like runScenario.
func parseScenarioArgs(name string, args []string, f *flag.FlagSet) (scenario.Scenario, error) {
	path := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		path, args = args[0], args[1:]
	}
	if err := f.Parse(args); err != nil {
		return scenario.Scenario{}, err
	}
	if path == "" && f.NArg() == 1 {
		path = f.Arg(0)
	} else if f.NArg() != 0 {
		return scenario.Scenario{}, errors.New("unexpected positional arguments")
	}
	if path == "" {
		return scenario.Scenario{}, errors.New("usage: mqtitan " + name + " SCENARIO")
	}
	s, err := scenario.Load(path)
	if err != nil {
		return s, err
	}
	if err = s.ResolveRun(); err != nil {
		return s, err
	}
	return s, thresholds.Validate(s.Thresholds)
}

func validateCommand(w io.Writer, args []string) error {
	f := flag.NewFlagSet("validate", flag.ContinueOnError)
	s, err := parseScenarioArgs("validate", args, f)
	if err != nil {
		return err
	}
	for _, msg := range s.Plan().Warnings {
		fmt.Fprintf(w, "WARN  workload             %s\n", msg)
	}
	for _, finding := range mqtt.CheckSecretFiles(s.Broker.TLS) {
		fmt.Fprintf(w, "WARN  secrets  %s: %s\n", finding.File, finding.Problem)
	}
	if s.Safety.StrictSecrets && s.Broker.TLS.KeyFile != "" {
		for _, finding := range mqtt.CheckSecretFiles(scenario.TLS{KeyFile: s.Broker.TLS.KeyFile}) {
			if !strings.HasPrefix(finding.Problem, "file not found") {
				return fmt.Errorf("refusing key file %s: %s (safety.strictSecrets)", finding.File, finding.Problem)
			}
		}
	}
	fmt.Fprintf(w, "VALID %s\n", s.Name)
	return nil
}

func planCommand(w io.Writer, args []string) error {
	f := flag.NewFlagSet("plan", flag.ContinueOnError)
	workers := f.Int("workers", 1, "worker count the clients are split across")
	resolve := f.Bool("resolve", false, "resolve the broker host to count destination addresses")
	s, err := parseScenarioArgs("plan", args, f)
	if err != nil {
		return err
	}
	if *workers < 1 || *workers > 256 {
		return errors.New("--workers must be between 1 and 256")
	}
	opts := scenario.PlanOptions{Workers: *workers}
	var resolveWarn string
	if *resolve {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		answers, err := mqtt.ResolveBroker(ctx, s.Broker)
		cancel()
		if err != nil || len(answers) == 0 {
			resolveWarn = fmt.Sprintf("WARN  dns  resolution failed; using %d assumed destination address(es)\n", max(len(s.Broker.AddressLabels), 1))
		} else {
			opts.Destinations = len(answers)
		}
	}
	printPlan(w, s, s.PlanWith(opts))
	fmt.Fprint(w, resolveWarn)
	return nil
}

func printPlan(w io.Writer, s scenario.Scenario, p scenario.Plan) {
	fmt.Fprintf(w, "Scenario: %s\nPeak clients: %d\nDuration: %s\nExpected publish rate: %.0f msg/s\nEstimated payload wire rate: %.2f MiB/s\nEstimated memory (uncalibrated): %.2f GiB\nEstimated file descriptors: %d\n", s.Name, p.PeakClients, p.Duration, p.MessagesPerSecond, p.EstimatedWireBytesPerSecond/1024/1024, float64(p.EstimatedMemoryBytes)/1024/1024/1024, p.EstimatedFileDescriptors)
	if l := s.Clients.Lifetime; l != nil {
		fmt.Fprintf(w, "Lifetime churn: %s to %s (~%.1f connects/s at peak)\n", l.Min.Duration, l.Max.Duration, p.ChurnConnectsPerSecond)
	}
	tlsScheme := strings.HasPrefix(s.Broker.URL, "mqtts://") || strings.HasPrefix(s.Broker.URL, "wss://")
	if tlsScheme {
		mode := s.Broker.TLS.HostnameVerification
		if mode == "" {
			mode = "san"
		}
		fmt.Fprintf(w, "TLS hostname verification: %s\n", mode)
	}
	if d := s.Broker.DNS; d != nil {
		var parts []string
		if d.ResolveEachDial {
			parts = append(parts, "resolveEachDial")
		}
		if len(d.Resolvers) > 0 {
			parts = append(parts, fmt.Sprintf("%d resolvers", len(d.Resolvers)))
		}
		if d.Pick != "" {
			parts = append(parts, "pick "+d.Pick)
		}
		if d.Family != "" && d.Family != "any" {
			parts = append(parts, d.Family)
		}
		fmt.Fprintf(w, "dns: %s\n", strings.Join(parts, ", "))
	}
	if n := len(s.Broker.AddressLabels); n > 0 {
		fmt.Fprintf(w, "Address labels: %d address label(s)\n", n)
	}
	for _, msg := range p.Warnings {
		fmt.Fprintf(w, "WARN  workload             %s\n", msg)
	}
	sf := s.Safety
	if len(sf.ForbiddenTopicPrefixes) > 0 || sf.ClientIDDenyRegex != "" {
		var parts []string
		if n := len(sf.ForbiddenTopicPrefixes); n > 0 {
			parts = append(parts, fmt.Sprintf("%d forbidden prefixes", n))
		}
		if sf.ClientIDDenyRegex != "" {
			parts = append(parts, fmt.Sprintf("client ID rule checked on %d IDs", s.Clients.Count))
		}
		fmt.Fprintf(w, "OK    safety  %s\n", strings.Join(parts, ", "))
	} else if tlsScheme && !sf.StrictSecrets {
		fmt.Fprintln(w, "WARN  safety  no forbiddenTopicPrefixes set")
	}
	for _, c := range platform.AssessPlan(platform.PlanInputs{Clients: int64(p.PeakClients), FileDescriptors: int64(p.EstimatedFileDescriptors), MemoryBytes: p.EstimatedMemoryBytes, WireBytesPerSecond: p.EstimatedWireBytesPerSecond}, platform.CurrentCapacity()) {
		fmt.Fprintf(w, "%-5s %-20s %s", c.Status, c.Name, c.Value)
		if c.Recommendation != "" {
			fmt.Fprintf(w, "  %s", c.Recommendation)
		}
		fmt.Fprintln(w)
	}
}
