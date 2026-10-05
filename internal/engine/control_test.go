package engine

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/scenario"
)

func TestLiveControlAdjustsClientsAndPausesPublishing(t *testing.T) {
	for _, version := range []string{"3.1.1", "5"} {
		t.Run(version, func(t *testing.T) {
			s := baseScenario(testBroker(t), version)
			s.Clients.Count = 4
			s.Workloads = []scenario.Workload{{Name: "pub", Type: "publisher", Clients: 4, Topic: "live", QoS: 1, RatePerClient: 0.01, Payload: scenario.Payload{Type: "static", Value: "live"}}}
			s.Stages = []scenario.Stage{{Duration: scenario.Duration{Duration: 200 * time.Millisecond}, TargetClients: 1}, {Duration: scenario.Duration{Duration: 4 * time.Second}, TargetClients: 4}}
			counters := metrics.New()
			control := NewControl(4)
			e := Engine{Scenario: s, Metrics: counters, Control: control}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- e.Run(ctx) }()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("engine did not stop")
				}
			}()
			wait := func(condition func() bool) {
				t.Helper()
				deadline := time.Now().Add(time.Second)
				for !condition() && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if !condition() {
					t.Fatal("live target not reached")
				}
			}
			wait(func() bool { return counters.Connected.Load() == 1 })
			if err := control.Apply(Load{Clients: 2, RatePerClient: 100, Revision: 1}); err != nil {
				t.Fatal(err)
			}
			wait(func() bool { return counters.Connected.Load() == 2 && counters.Published.Load() >= 10 })
			time.Sleep(220 * time.Millisecond)
			if counters.Connected.Load() != 2 {
				t.Fatal("next scenario stage overwrote live target")
			}
			if err := control.Apply(Load{Clients: 2, RatePerClient: 0, Revision: 2}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(25 * time.Millisecond)
			paused := counters.Published.Load()
			time.Sleep(60 * time.Millisecond)
			if counters.Published.Load() != paused || counters.Connected.Load() != 2 {
				t.Fatal("pause failed or disconnected clients")
			}
			if err := control.Apply(Load{Clients: 0, RatePerClient: 20, Revision: 3}); err != nil {
				t.Fatal(err)
			}
			wait(func() bool { return counters.Connected.Load() == 0 })
			if err := control.Apply(Load{Clients: 4, RatePerClient: 100, Revision: 4}); err != nil {
				t.Fatal(err)
			}
			wait(func() bool { return counters.Connected.Load() == 4 && counters.Published.Load() > paused+10 })
			if counters.PublishErrors.Load() != 0 {
				t.Fatal("intentional load changes counted as errors")
			}
		})
	}
}

func TestLiveControlBoundsAndLatestValueNotification(t *testing.T) {
	control := NewControl(10)
	for _, load := range []Load{{Clients: -1, Revision: 1}, {Clients: 11, Revision: 1}, {Clients: 1, RatePerClient: -1, Revision: 1}, {Clients: 1, RatePerClient: math.NaN(), Revision: 1}, {Clients: 1, RatePerClient: math.Inf(1), Revision: 1}, {Clients: 1, RatePerClient: 1e6 + 1, Revision: 1}, {Clients: 1}} {
		if err := control.Apply(load); err == nil {
			t.Fatal("invalid live control accepted")
		}
	}
	_, changed := control.Current()
	if err := control.Apply(Load{Clients: 5, RatePerClient: 10, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("listeners not notified")
	}
	if err := control.Apply(Load{Clients: 2, RatePerClient: 1, Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if err := control.Apply(Load{Clients: 9, RatePerClient: 99, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	load, _ := control.Current()
	if load.Clients != 2 || load.Revision != 2 {
		t.Fatal("stale command changed load")
	}
}

func BenchmarkControlUnchanged(b *testing.B) {
	control := NewControl(1000000)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		control.Current()
	}
}
