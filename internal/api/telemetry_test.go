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
	m.SetTelemetryAllowedHosts("127.0.0.1")
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
	raw, err := m.store.Get("tests", test.ID)
	if err != nil || strings.Contains(string(raw), `"source"`) || strings.Contains(string(raw), `"brokers"`) {
		t.Fatalf("samples stored on the test record (%v)", err)
	}
	r := httptest.NewRequest("GET", "/api/v1/tests/missing/brokers", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
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
	m.SetTelemetryAllowedHosts("127.0.0.1")
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

func TestAPITelemetryHostAllowlist(t *testing.T) {
	var hits atomic.Int64
	var auth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`[{"node":"n","live_connections":1}]`))
	}))
	defer srv.Close()
	raw := strings.Replace(telemetryScenario(srv.URL, "none", ""), "duration: 150ms", "duration: 2s", 1)

	m, _ := setup(t)
	m.SetCredentials(emqx.Credentials{APIKey: "k", APISecret: "s"})
	// Empty allowlist rejects API submissions.
	if _, err := m.Start(raw); err == nil || !strings.Contains(err.Error(), "is not in MQTITAN_TELEMETRY_ALLOWED_HOSTS") {
		t.Fatalf("empty allowlist: %v", err)
	}
	m.SetTelemetryAllowedHosts("other.example.net, 127.0.0.2")
	if _, err := m.Start(raw); err == nil || !strings.Contains(err.Error(), "telemetry host 127.0.0.1:") {
		t.Fatalf("other host: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if hits.Load() != 0 {
		t.Fatal("rejected scenario contacted the telemetry server")
	}
	// Allowlisted host is accepted and receives credentials.
	m.SetTelemetryAllowedHosts("127.0.0.1")
	test, err := m.Start(raw)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "request", func() bool { return hits.Load() > 0 })
	if a, _ := auth.Load().(string); !strings.HasPrefix(a, "Basic ") {
		t.Fatalf("auth %q", a)
	}
	_ = m.Stop(test.ID)
	waitFor(t, "stop", func() bool { v, _ := m.Get(test.ID); return v.Status != "running" })

	// Local runs are exempt from the allowlist.
	m2, _ := setup(t)
	m2.SetCredentials(emqx.Credentials{APIKey: "k", APISecret: "s"})
	before := hits.Load()
	local, err := m2.StartLocal(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "local request", func() bool { return hits.Load() > before })
	_ = m2.Stop(local.ID)
	waitFor(t, "local stop", func() bool { v, _ := m2.Get(local.ID); return v.Status != "running" })
}

func TestBalanceThresholdUsesPeakSample(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/nodes" {
			w.WriteHeader(500)
			return
		}
		n := 0
		if calls.Add(1) == 1 {
			n = 300
		}
		_, _ = fmt.Fprintf(w, `[{"node":"n","live_connections":%d}]`, n)
	}))
	defer srv.Close()
	m, _ := setup(t)
	m.SetCredentials(emqx.Credentials{APIKey: "k", APISecret: "s"})
	m.SetTelemetryAllowedHosts("127.0.0.1")
	m.telemetryEvery = 50 * time.Millisecond
	long := strings.Replace(telemetryScenario(srv.URL, "none", ""), "duration: 150ms", "duration: 1s", 1)
	long = strings.Replace(long, "workloads:", "thresholds:\n  broker_connection_cv: \"<10%\"\nworkloads:", 1)
	test, err := m.Start(long)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "two samples", func() bool { v, _ := m.Brokers(test.ID); return len(v) >= 2 })
	if err = m.Stop(test.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop", func() bool { v, _ := m.Get(test.ID); return v.Status != "running" })
	got, _ := m.Get(test.ID)
	if got.Balance == nil || len(got.Balance.Targets) != 1 || got.Balance.Targets[0].Connections != 300 {
		t.Fatalf("%+v", got.Balance)
	}
	if len(got.Thresholds) != 1 || got.Thresholds[0].Observed != "no observations" {
		t.Fatalf("%+v", got.Thresholds)
	}
}

func TestPrometheusBalanceSeries(t *testing.T) {
	m, h := setup(t)
	old := Test{ID: "old", Name: "o", Status: "completed", StartedAt: time.Now().Add(-time.Hour)}
	cur := Test{ID: "cur", Name: "c", Status: "completed", StartedAt: time.Now(), Balance: &emqx.Balance{Complete: false, TargetCV: 0.25, ZoneSkew: 0.5,
		Targets: []emqx.Group{{Key: "e0", Connections: 1}, {Key: "e1", Connections: 3}}, Zones: []emqx.Group{{Key: "a", Connections: 1}, {Key: "b", Connections: 3}}}}
	for _, x := range []Test{old, cur} {
		if err := m.persist(x); err != nil {
			t.Fatal(err)
		}
	}
	samples := []emqx.FleetSample{
		{Baseline: true, Targets: []emqx.TargetSample{{Target: "e0", Zone: "a", Connections: 999}}},
		{Complete: true, Targets: []emqx.TargetSample{{Target: "e0", Zone: "a", Connections: 7}, {Target: "e1", Zone: "b", Connections: 9}}},
		{Complete: false, Targets: []emqx.TargetSample{{Target: "e0", Zone: "a", Connections: 11}, {Target: "e1", Zone: "b", Connections: 13}, {Error: "no label"}}},
	}
	b, _ := json.Marshal(samples)
	if err := m.store.Put("brokers", "cur", b); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{
		`emqxload_broker_connections{target="e0",zone="a"} 11`, `emqxload_broker_connections{target="e1",zone="b"} 13`,
		`emqxload_broker_connection_cv 0.25`, `emqxload_zone_connection_skew_ratio 0.5`, `emqxload_broker_telemetry_complete 0`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Fatalf("missing %s in\n%s", want, body)
		}
	}
	if strings.Contains(body, "999") || strings.Contains(body, `target=""`) {
		t.Fatal("baseline or errored target exported")
	}
}

func TestPrometheusOmitsUndefinedBalance(t *testing.T) {
	m, h := setup(t)
	if err := m.persist(Test{ID: "zero", Name: "z", Status: "completed", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]emqx.FleetSample{{Complete: true, Targets: []emqx.TargetSample{{Target: "e0", Connections: 0}, {Target: "e1", Connections: 0}}}})
	if err := m.store.Put("brokers", "zero", b); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	if strings.Contains(body, "emqxload_broker_connection_cv") || strings.Contains(body, "emqxload_zone_connection_skew_ratio") {
		t.Fatal(body)
	}
	if !strings.Contains(body, "emqxload_broker_telemetry_complete 1\n") {
		t.Fatal(body)
	}
}
