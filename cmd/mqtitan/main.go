package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/observability"
	"github.com/mqtitan/mqtitan/internal/platform"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/thresholds"
	"log/slog"
	"os"
	"os/signal"
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
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	}()
	if err := run(os.Args[1:]); err != nil {
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
	case "validate", "plan":
		if len(args) != 2 {
			return errors.New("usage: mqtitan " + args[0] + " SCENARIO")
		}
		s, err := scenario.Load(args[1])
		if err != nil {
			return err
		}
		if err = thresholds.Validate(s.Thresholds); err != nil {
			return err
		}
		if args[0] == "validate" {
			fmt.Printf("VALID %s\n", s.Name)
		} else {
			printPlan(s)
		}
		return nil
	case "run":
		return runScenario(args[1:])
	case "quick":
		return quick(args[1:])
	case "serve":
		return serve(args[1:])
	case "worker":
		return workerCommand(args[1:])
	case "status", "stop", "results", "export", "workers":
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
	fmt.Fprintln(os.Stderr, "usage: mqtitan <quick|validate|plan|run|serve|worker|status|stop|results|export|workers|doctor|version>\nquick --broker mqtt://localhost:1883 --clients 10000 --rate 1\nserve --listen 127.0.0.1:8080 --data mqtitan.db\nrun scenario.yaml [--controller http://localhost:8080] [--workers worker-a,worker-b]\n\nEnvironment: MQTITAN_CONTROLLER (default --controller), MQTITAN_TOKEN (bearer token), MQTITAN_MQTT_PASSWORD")
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
func printPlan(s scenario.Scenario) {
	p := s.Plan()
	fmt.Printf("Scenario: %s\nPeak clients: %d\nDuration: %s\nExpected publish rate: %.0f msg/s\nEstimated payload wire rate: %.2f MiB/s\nEstimated memory (uncalibrated): %.2f GiB\nEstimated file descriptors: %d\n", s.Name, p.PeakClients, p.Duration, p.MessagesPerSecond, p.EstimatedWireBytesPerSecond/1024/1024, float64(p.EstimatedMemoryBytes)/1024/1024/1024, p.EstimatedFileDescriptors)
	for _, c := range platform.AssessPlan(platform.PlanInputs{Clients: int64(p.PeakClients), FileDescriptors: int64(p.EstimatedFileDescriptors), MemoryBytes: p.EstimatedMemoryBytes, WireBytesPerSecond: p.EstimatedWireBytesPerSecond}, platform.CurrentCapacity()) {
		fmt.Printf("%-5s %-20s %s", c.Status, c.Name, c.Value)
		if c.Recommendation != "" {
			fmt.Printf("  %s", c.Recommendation)
		}
		fmt.Println()
	}
}
