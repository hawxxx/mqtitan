package worker

import (
	"context"
	"fmt"
	broker "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mqtitan/mqtitan/internal/api"
	"github.com/mqtitan/mqtitan/internal/storage"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTwoWorkersExecuteSameScenarioWithoutDuplicateClients(t *testing.T) {
	b := broker.New(&broker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := b.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	l := listeners.NewTCP(listeners.Config{ID: "distributed", Address: "127.0.0.1:0"})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	if err := b.Serve(); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
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
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = Run(ctx, Options{Controller: controller.URL, ID: id, Token: "test-secret"})
		}(id)
	}
	raw := fmt.Sprintf(`apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: distributed wire
broker:
  url: mqtt://%s
  version: "5"
  connectTimeout: 1s
  keepAlive: 10s
  cleanStart: true
clients:
  count: 4
  idTemplate: distributed-${sequence}
stages:
  - duration: 1500ms
    targetClients: 4
workloads:
  - name: publishers
    type: publisher
    clients: 4
    topic: test/${clientId}
    qos: 1
    ratePerClient: 20
    payload:
      type: static
      value: hello
thresholds:
  connection_error_rate: "<1%%"
`, l.Address())
	var test api.Test
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		test, err = m.StartOnWorkers(raw, []string{"a", "b"})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		test, err = m.Get(test.ID)
		if err != nil {
			t.Fatal(err)
		}
		if test.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if test.Status != "completed" || test.Snapshot.ConnectAttempts != 4 || test.Snapshot.Published < 20 || test.Snapshot.Connected != 0 || test.Snapshot.P95 <= 0 {
		t.Fatalf("distributed result: %+v", test)
	}
}
