package worker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mqtitan/mqtitan/internal/api"
	"github.com/mqtitan/mqtitan/internal/storage"
)

func TestTwoWorkersApplyLiveClientAndRateChanges(t *testing.T) {
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := b.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	l := listeners.NewTCP(listeners.Config{ID: "live", Address: "127.0.0.1:0"})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	if err := b.Serve(); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	store, err := storage.Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m, err := api.NewManager(store)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	controller := httptest.NewServer(api.Handler(m, api.Options{Token: "test-secret"}))
	defer controller.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var group sync.WaitGroup
	defer func() { cancel(); group.Wait() }()
	for _, id := range []string{"a", "b"} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			_ = Run(ctx, Options{Controller: controller.URL, ID: id, Token: "test-secret"})
		}(id)
	}
	raw := fmt.Sprintf(`apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: live distributed
broker:
  url: mqtt://%s
  version: "5"
  connectTimeout: 1s
  cleanStart: true
clients:
  count: 6
  idTemplate: live-${sequence}
stages:
  - duration: 40s
    targetClients: 2
workloads:
  - name: publishers
    type: publisher
    clients: 6
    topic: live/${clientId}
    qos: 1
    ratePerClient: 1
    payload:
      type: static
      value: hello
`, l.Address())
	var run api.Test
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		run, err = m.StartOnWorkers(raw, []string{"a", "b"})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	wait := func(check func(api.Test) bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			run, err = m.Get(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if check(run) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("live load not reached: status=%s error=%s clients=%d attempts=%d errors=%d target=%d revision=%d pending=%v", run.Status, run.Error, run.Snapshot.Connected, run.Snapshot.ConnectAttempts, run.Snapshot.ConnectErrors, run.Snapshot.TargetClients, run.LoadControl.Revision, run.LoadControl.PendingWorkers)
	}
	wait(func(r api.Test) bool { return r.Snapshot.Connected == 2 })
	apply := func(clients int, rate float64) {
		t.Helper()
		run, err = m.UpdateLoad(run.ID, api.LoadRequest{Clients: clients, RatePerClient: rate, Revision: run.LoadControl.Revision})
		if err != nil {
			t.Fatal(err)
		}
		wait(func(r api.Test) bool {
			return len(r.LoadControl.PendingWorkers) == 0 && r.Snapshot.Connected == int64(clients)
		})
	}
	apply(5, 100)
	if run.Snapshot.Published < 20 {
		t.Fatal("increased rate not observed")
	}
	apply(5, 0)
	time.Sleep(1100 * time.Millisecond) // Let the final pre-pause cumulative sample arrive.
	run, err = m.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	published := run.Snapshot.Published
	time.Sleep(1100 * time.Millisecond)
	run, err = m.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Snapshot.Published != published {
		t.Fatal("distributed publishing did not pause")
	}
	apply(0, 20)
	apply(4, 50)
	if run.Snapshot.Connected != 4 || len(run.LoadChanges) != 4 {
		t.Fatal("resume/history failed")
	}
	if err = m.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	wait(func(r api.Test) bool { return r.Status == "stopped" })
	if run.Snapshot.Connected != 0 || run.Snapshot.PublishErrors != 0 || run.Snapshot.ConnectErrors != 0 {
		t.Fatal("live changes leaked connections or counted intentional errors")
	}
}
