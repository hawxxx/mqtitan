package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/emqx"
)

func telemetryScenario(url, baseline, stage string) string {
	return strings.Replace(testScenario, "workloads:", fmt.Sprintf("telemetry:\n  baseline: %s\n  expectedTargets: 1\n  emqx:\n    - name: emqx-0\n      zone: zone-a\n      url: %s\nworkloads:", baseline, url), 1)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTestTelemetryPolledAndStreamed(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/nodes" {
			_, _ = fmt.Fprintf(w, `[{"node":"n","live_connections":%d}]`, 10*calls.Add(1))
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	m, h := setup(t)
	m.SetCredentials(emqx.Credentials{APIKey: "k", APISecret: "s"})
	m.telemetryEvery = 50 * time.Millisecond
	long := strings.Replace(telemetryScenario(srv.URL, "none", ""), "duration: 150ms", "duration: 3s", 1)
	test, err := m.Start(long)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub, err := m.Subscribe(test.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	var got emqx.FleetSample
	deadline := time.After(10 * time.Second)
	for got.Source == "" {
		select {
		case e := <-ch:
			if e.Kind == "broker" {
				got = e.Data.(emqx.FleetSample)
			}
		case <-deadline:
			t.Fatal("no broker event")
		}
	}
	if got.Source != "emqx" || len(got.Targets) != 1 || got.Targets[0].Target != "emqx-0" || got.Targets[0].Zone != "zone-a" || !got.Complete {
		t.Fatalf("%+v", got)
	}
	// Live route.
	get := func() (int, []emqx.FleetSample) {
		r := httptest.NewRequest("GET", "/api/v1/tests/"+test.ID+"/brokers", nil)
		r.Header.Set("Authorization", "Bearer controller-secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var env struct {
			Data []emqx.FleetSample `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("%v: %s", err, w.Body.String())
		}
		return w.Code, env.Data
	}
	if code, v := get(); code != 200 || len(v) == 0 {
		t.Fatalf("live: %d %v", code, v)
	}
	waitFor(t, "two samples", func() bool { v, _ := m.Brokers(test.ID); return len(v) >= 2 })
	if err := m.Stop(test.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop", func() bool { v, _ := m.Get(test.ID); return v.Status != "running" })
	// Persisted samples survive and are not embedded in the public test record.
	if code, v := get(); code != 200 || len(v) < 2 {
		t.Fatalf("persisted: %d %d samples", code, len(v))
	}
	b, _ := json.Marshal(mustGet(t, m, test.ID))
	if strings.Contains(string(b), "brokers") {
		t.Fatal("samples leaked into public test record")
	}
	r := httptest.NewRequest("GET", "/api/v1/tests/missing/brokers", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
}

func mustGet(t *testing.T, m *Manager, id string) Test {
	t.Helper()
	v, err := m.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBaselineSampleTakenBeforeEngineStarts(t *testing.T) {
	var m *Manager
	var calls atomic.Int64
	var once sync.Once
	var attemptsAtBaseline atomic.Int64
	attemptsAtBaseline.Store(-1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/nodes" {
			w.WriteHeader(500)
			return
		}
		n := calls.Add(1)
		once.Do(func() {
			// Give an engine that started early time to attempt connections.
			time.Sleep(150 * time.Millisecond)
			m.mu.Lock()
			for _, run := range m.runs {
				attemptsAtBaseline.Store(int64(run.counters.Snapshot().ConnectAttempts))
			}
			m.mu.Unlock()
		})
		_, _ = fmt.Fprintf(w, `[{"node":"n","live_connections":%d}]`, 100*n)
	}))
	defer srv.Close()
	m, _ = setup(t)
	m.SetCredentials(emqx.Credentials{APIKey: "k", APISecret: "s"})
	m.telemetryEvery = 50 * time.Millisecond
	long := strings.Replace(telemetryScenario(srv.URL, "first", ""), "duration: 150ms", "duration: 2s", 1)
	test, err := m.Start(long)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second sample", func() bool { v, _ := m.Brokers(test.ID); return len(v) >= 2 })
	_ = m.Stop(test.ID)
	if a := attemptsAtBaseline.Load(); a != 0 {
		t.Fatalf("engine had %d connect attempts when the baseline was fetched", a)
	}
	v, _ := m.Brokers(test.ID)
	if !v[0].Baseline || v[1].Baseline {
		t.Fatalf("baseline flags: %+v", v[:2])
	}
	if c := v[1].Targets[0]; c.Raw != 200 || c.Connections != 100 {
		t.Fatalf("baseline not subtracted: %+v", c)
	}
}
