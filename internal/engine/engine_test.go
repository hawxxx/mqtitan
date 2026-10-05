package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mqtitan/mqtitan/internal/metrics"
	transport "github.com/mqtitan/mqtitan/internal/mqtt"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func testBroker(t *testing.T) string {
	t.Helper()
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := b.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	l := listeners.NewTCP(listeners.Config{ID: "test", Address: "127.0.0.1:0"})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	if err := b.Serve(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return "mqtt://" + l.Address()
}

func TestSharedCorrelationAcrossEngines(t *testing.T) {
	url := testBroker(t)
	sub := baseScenario(url, "5")
	sub.SharedCorrelationID = "00112233445566778899aabbccddeeff"
	sub.Clients.Count = 1
	sub.Workloads = sub.Workloads[:1]
	sub.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 200 * time.Millisecond}, TargetClients: 1}}
	pub := sub
	pub.Clients.SequenceOffset = 1
	pub.Workloads = []scenario.Workload{{Type: "publisher", Clients: 1, Topic: "wire", RatePerClient: 100, Correlate: true, Payload: scenario.Payload{Type: "static", Value: "shared"}}}
	m := metrics.New()
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a := Engine{Scenario: sub, Metrics: m}
	go func() { done <- a.Run(ctx) }()
	for m.Subscriptions.Load() == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("subscriber not started")
		case <-time.After(time.Millisecond):
		}
	}
	b := Engine{Scenario: pub}
	if err := b.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m.CorrelationSamples.Load() < 5 {
		t.Fatalf("cross-engine samples %+v", m.Snapshot())
	}
}

func TestReconnectRestoresPublishingAfterPeerLoss(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			_ = b.AddHook(new(auth.AllowHook), nil)
			l := listeners.NewTCP(listeners.Config{ID: "tcp", Address: "127.0.0.1:0"})
			if err := b.AddListener(l); err != nil {
				t.Fatal(err)
			}
			_ = b.Serve()
			defer b.Close()
			s := baseScenario("mqtt://"+l.Address(), v)
			s.Clients.Count = 1
			s.Workloads = []scenario.Workload{{Type: "mixed", Clients: 1, Topic: "retry", QoS: 1, RatePerClient: 100, Payload: scenario.Payload{Type: "static", Value: "retry"}}}
			s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 250 * time.Millisecond}, TargetClients: 1}}
			s.Broker.Reconnect = scenario.Reconnect{Enabled: true, MaxAttempts: 2, InitialBackoff: scenario.Duration{Duration: 10 * time.Millisecond}, MaxBackoff: scenario.Duration{Duration: 20 * time.Millisecond}}
			m := metrics.New()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			e := Engine{Scenario: s, Metrics: m}
			go func() { done <- e.Run(ctx) }()
			for m.Published.Load() < 3 {
				select {
				case <-ctx.Done():
					t.Fatal("no initial publish")
				case <-time.After(time.Millisecond):
				}
			}
			cl, ok := b.Clients.Get("wire-1")
			if !ok {
				t.Fatal("broker lacks client")
			}
			_ = cl.Net.Conn.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			got := m.Snapshot()
			if got.ConnectAttempts != 2 || got.Reconnects != 1 || got.Subscriptions != 2 || got.Published < 10 || got.ConnectP95 <= 0 {
				t.Fatalf("reconnect failed %+v", got)
			}
		})
	}
}

func TestReconnectAttemptsAreBounded(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	s := baseScenario("mqtt://"+addr, "5")
	s.Clients.Count = 1
	s.Workloads = []scenario.Workload{{Type: "connection", Clients: 1}}
	s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 100 * time.Millisecond}, TargetClients: 1}}
	s.Broker.Reconnect = scenario.Reconnect{Enabled: true, MaxAttempts: 2, InitialBackoff: scenario.Duration{Duration: time.Millisecond}, MaxBackoff: scenario.Duration{Duration: 2 * time.Millisecond}}
	m := metrics.New()
	e := Engine{Scenario: s, Metrics: m}
	if err = e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := m.Snapshot()
	if got.ConnectAttempts != 3 || got.ConnectErrors != 3 || got.Errors["connect"] != 3 {
		t.Fatalf("unbounded/missing retries %+v", got)
	}
}

func TestTemplatesUseGlobalSequenceAndWorkerInSizedPayload(t *testing.T) {
	url := testBroker(t)
	s := baseScenario(url, "5")
	s.Clients.Count = 1
	s.Clients.SequenceOffset = 10
	s.Clients.Worker = "west"
	s.Workloads = []scenario.Workload{{Type: "publisher", Clients: 1, Topic: "template/${sequence}", RatePerClient: 100, Payload: scenario.Payload{Type: "static", Value: "${worker}-${sequence}-${clientId}", Size: 40}}}
	s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 80 * time.Millisecond}, TargetClients: 1}}
	received := make(chan []byte, 10)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, err := transport.Connect(ctx, s.Broker, "observer", func(b []byte) { received <- append([]byte(nil), b...) })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err = sub.Subscribe(ctx, "template/11", 0); err != nil {
		t.Fatal(err)
	}
	e := Engine{Scenario: s}
	if err = e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-received:
		if len(b) != 40 || string(bytes.TrimRight(b, "\x00")) != "west-11-wire-11" {
			t.Fatalf("payload %q", b)
		}
	default:
		t.Fatal("global sequence topic not received")
	}
}

func TestSizedJSONPayloadIsDeliveredAsJSON(t *testing.T) {
	url := testBroker(t)
	s := baseScenario(url, "5")
	s.Clients.Count = 1
	s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 120 * time.Millisecond}, TargetClients: 1}}
	s.Workloads = []scenario.Workload{{Type: "publisher", Clients: 1, Topic: "json", QoS: 1, RatePerClient: 100, Payload: scenario.Payload{Type: "json", Size: 512, Value: `{"sequence":"${counter}","temperature":"${random.float:60:95}"}`}}}
	received := make(chan []byte, 32)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sub, err := transport.Connect(ctx, s.Broker, "json-observer", func(b []byte) { received <- append([]byte(nil), b...) })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err = sub.Subscribe(ctx, "json", 1); err != nil {
		t.Fatal(err)
	}
	e := Engine{Scenario: s}
	if err = e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-received:
		if len(b) != 512 || !json.Valid(b) {
			t.Fatalf("invalid JSON on wire: %q", b)
		}
	default:
		t.Fatal("no JSON delivered")
	}
}

func TestDoesNotCorrelateForeignMessages(t *testing.T) {
	url := testBroker(t)
	s := baseScenario(url, "5")
	s.Clients.Count = 1
	s.Workloads = s.Workloads[:1]
	s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 200 * time.Millisecond}, TargetClients: 1}}
	m := metrics.New()
	e := Engine{Scenario: s, Metrics: m}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	for m.Subscriptions.Load() == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("subscriber not started")
		case <-time.After(time.Millisecond):
		}
	}
	pub, err := transport.Connect(ctx, s.Broker, "foreign", func([]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 24)
	copy(data, "MQTITAN1")
	binary.BigEndian.PutUint64(data[8:16], uint64(time.Now().UnixNano()))
	if err = pub.Publish(ctx, "wire", 0, false, data); err != nil {
		t.Fatal(err)
	}
	pub.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if m.Received.Load() == 0 || m.CorrelationSamples.Load() != 0 {
		t.Fatalf("foreign correlation %+v", m.Snapshot())
	}
}
func baseScenario(url, version string) scenario.Scenario {
	return scenario.Scenario{APIVersion: "mqtitan.io/v1alpha1", Kind: "Scenario", Name: "wire", Broker: scenario.Broker{URL: url, Version: version, ConnectTimeout: scenario.Duration{Duration: time.Second}, KeepAlive: scenario.Duration{Duration: 10 * time.Second}, CleanStart: true}, Clients: scenario.Clients{Count: 2, IDTemplate: "wire-${sequence}"}, Stages: []scenario.Stage{{Duration: scenario.Duration{Duration: 150 * time.Millisecond}, TargetClients: 2}, {Duration: scenario.Duration{Duration: 100 * time.Millisecond}, TargetClients: 1}, {Duration: scenario.Duration{Duration: 150 * time.Millisecond}, TargetClients: 2}}, Workloads: []scenario.Workload{{Type: "subscriber", Clients: 1, Topic: "wire", QoS: 1}, {Type: "publisher", Clients: 1, Topic: "wire", QoS: 1, RatePerClient: 100, Payload: scenario.Payload{Type: "static", Value: "hello"}, Correlate: true}}}
}
func TestStagesRetainClientsAndDeliverOnBothProtocols(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			m := metrics.New()
			s := baseScenario(testBroker(t), v)
			e := Engine{Scenario: s, Metrics: m, ConnectionRate: 1000}
			if err := e.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := m.Snapshot()
			if got.ConnectAttempts != 3 {
				t.Errorf("retained client was reconnected: %+v", got)
			}
			if got.Received < 10 || got.Published < 10 {
				t.Errorf("no delivery: %+v", got)
			}
			if got.CorrelationSamples == 0 {
				t.Error("no end to end latency")
			}
			if got.Connected != 0 || got.Connecting != 0 {
				t.Errorf("resources remain: %+v", got)
			}
		})
	}
}
func TestCancellationJoinsWorkers(t *testing.T) {
	s := baseScenario(testBroker(t), "3.1.1")
	s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: time.Hour}, TargetClients: 2}}
	m := metrics.New()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	e := Engine{Scenario: s, Metrics: m}
	if err := e.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v", err)
	}
	a := m.Snapshot()
	time.Sleep(30 * time.Millisecond)
	b := m.Snapshot()
	if a.PublishAttempts != b.PublishAttempts || b.Connected != 0 {
		t.Errorf("worker escaped: %+v %+v", a, b)
	}
}

func TestMixedQoSZeroAndTwo(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		for _, q := range []byte{0, 2} {
			t.Run(fmt.Sprintf("%s/qos%d", v, q), func(t *testing.T) {
				s := baseScenario(testBroker(t), v)
				s.Clients.Count = 1
				s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 120 * time.Millisecond}, TargetClients: 1}}
				s.Workloads = []scenario.Workload{{Type: "mixed", Clients: 1, Topic: "mixed/${sequence}", QoS: q, RatePerClient: 100, Payload: scenario.Payload{Type: "json", Value: `{"device":"${clientId}"}`}, Correlate: true}}
				m := metrics.New()
				e := Engine{Scenario: s, Metrics: m}
				if err := e.Run(context.Background()); err != nil {
					t.Fatal(err)
				}
				got := m.Snapshot()
				if got.Published < 5 || got.Received < 5 || got.PublishErrors != 0 || got.CorrelationSamples < 5 {
					t.Fatalf("mixed QoS not delivered %+v", got)
				}
			})
		}
	}
}

func TestIntentionalShutdownDoesNotCountAsPublishFailure(t *testing.T) {
	for _, v := range []string{"3.1.1", "5"} {
		t.Run(v, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				buf := make([]byte, 4096)
				_, _ = c.Read(buf)
				ack := []byte{0x20, 2, 0, 0}
				if v == "5" {
					ack = []byte{0x20, 3, 0, 0, 0}
				}
				_, _ = c.Write(ack)
				_, _ = io.Copy(io.Discard, c)
			}()
			s := baseScenario("mqtt://"+l.Addr().String(), v)
			s.Clients.Count = 1
			s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 100 * time.Millisecond}, TargetClients: 1}}
			s.Workloads = []scenario.Workload{{Type: "publisher", Clients: 1, Topic: "unacked", QoS: 1, RatePerClient: 100, Payload: scenario.Payload{Value: "hello"}}}
			m := metrics.New()
			e := Engine{Scenario: s, Metrics: m}
			if err = e.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			snap := m.Snapshot()
			if snap.PublishAttempts != 1 || snap.PublishErrors != 0 {
				t.Fatalf("intentional cancellation counted as broker error: attempts=%d errors=%d", snap.PublishAttempts, snap.PublishErrors)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("socket leaked")
			}
		})
	}
}
