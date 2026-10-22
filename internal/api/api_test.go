package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/storage"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testScenario = `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: API test
broker:
  url: mqtt://127.0.0.1:1
  version: "3.1.1"
  connectTimeout: 100ms
  keepAlive: 30s
  cleanStart: true
  password: private-password
clients:
  count: 1
  idTemplate: api-${sequence}
stages:
  - duration: 150ms
    targetClients: 1
workloads:
  - name: publisher
    type: publisher
    clients: 1
    topic: test
    qos: 0
    ratePerClient: 1
    payload:
      type: static
      value: hello
`

func setup(t *testing.T) (*Manager, http.Handler) {
	t.Helper()
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, Handler(m, Options{Token: "controller-secret"})
}

func TestPrometheusExportsReceivedCountersAndRealHistogram(t *testing.T) {
	m, h := setup(t)
	test := Test{ID: "metric-run", Name: "metric", Status: "completed", Snapshot: metrics.Snapshot{Published: 2, Received: 3, BytesReceived: 900, PublishLatencyCount: 2, PublishLatencySum: 6 * time.Millisecond, Histogram: []metrics.Bucket{{UpperBoundNs: time.Millisecond, Count: 1}, {UpperBoundNs: 10 * time.Millisecond, Count: 1}}}}
	if err := m.persist(test); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, want := range []string{"emqxload_messages_received_total 3", "emqxload_bytes_received_total 900", "emqxload_publish_latency_seconds_bucket{le=\"+Inf\"} 2", "emqxload_publish_latency_seconds_count 2"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestAuthAndInvalidScenario(t *testing.T) {
	_, h := setup(t)
	for _, tc := range []struct {
		path, token string
		want        int
	}{{"/api/v1/tests", "", 401}, {"/api/v1/tests", "wrong", 401}, {"/api/v1/tests", "controller-secret", 200}, {"/healthz", "", 200}} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s got %d", tc.path, w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/tests", strings.NewReader(`{"scenario":"nonsense"}`))
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("invalid scenario: %d %s", w.Code, w.Body.String())
	}
}

func TestRunSurvivesRequestAndRedactsSecrets(t *testing.T) {
	m, h := setup(t)
	body, _ := json.Marshal(map[string]string{"scenario": testScenario})
	r := httptest.NewRequest("POST", "/api/v1/tests", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-password") {
		t.Fatal("API leaks password")
	}
	var response struct {
		Data Test `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		test, err := m.Get(response.Data.ID)
		if err != nil {
			t.Fatal(err)
		}
		if test.Status != "running" {
			if test.Status != "failed" {
				t.Fatalf("unreachable broker reported success: %s", test.Status)
			}
			if test.Snapshot.ConnectAttempts != 1 {
				t.Fatalf("did not execute: %+v", test)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("test never finished")
}

func TestRestartMarksRunInterrupted(t *testing.T) {
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, _ := json.Marshal(Test{ID: "abandoned", Status: "running", Name: "recovered"})
	if err = s.Put("tests", "abandoned", raw); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(s)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	v, err := m.Get("abandoned")
	if err != nil || v.Status != "interrupted" {
		t.Fatalf("restart: %+v %v", v, err)
	}
	_ = context.Background()
}

func TestLocalEngineSetupErrorIsReported(t *testing.T) {
	m, _ := setup(t)
	raw := strings.Replace(testScenario, "url: mqtt://127.0.0.1:1", "url: mqtts://127.0.0.1:1\n  tls:\n    caFile: "+filepath.Join(t.TempDir(), "missing-ca.pem"), 1)
	started, err := m.StartLocal(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := m.Get(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "running" {
			if got.Status != "failed" || got.Error != "load engine failed: TLS CA file cannot be read" {
				t.Fatalf("setup failure hidden: %s %q", got.Status, got.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	list, err := m.List()
	if err != nil || len(list) != 1 || list[0].ID != started.ID || strings.Contains(list[0].Scenario, "private-password") {
		t.Fatalf("list %+v %v", list, err)
	}
}

func TestPerSecondRejectsRegressionAndEmptyIntervals(t *testing.T) {
	if got := perSecond(150, 50, 2); got != 50 {
		t.Fatalf("rate = %v, want 50", got)
	}
	// A lost worker report shrinks the aggregate; it must not wrap to ~1.8e19.
	if got := perSecond(10, 50, 1); got != 0 {
		t.Fatalf("regressed counter rate = %v, want 0", got)
	}
	if got := perSecond(10, 5, 0); got != 0 {
		t.Fatalf("zero interval rate = %v, want 0", got)
	}
}

func TestPrometheusConnectFailureSeries(t *testing.T) {
	m, h := setup(t)
	for i, snap := range []metrics.Snapshot{
		{ConnectErrors: 5, ConnectFailures: map[string]uint64{"tcp": 2, "mqtt_connack": 3}, ConnackReasons: map[string]uint64{"135": 2, "5": 1}},
		{ConnectErrors: 2, ConnectFailures: map[string]uint64{"tcp": 1, "tls_alert": 1}, ConnackReasons: map[string]uint64{"135": 1}},
	} {
		if err := m.persist(Test{ID: fmt.Sprintf("failures-%d", i), Name: "f", Status: "completed", Snapshot: snap}); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{
		`emqxload_connect_failures_total{stage="tcp"} 3`, `emqxload_connect_failures_total{stage="mqtt_connack"} 3`,
		`emqxload_connect_failures_total{stage="tls_alert"} 1`, `emqxload_connect_failures_total{stage="dns"} 0`,
		`emqxload_connect_failures_total{stage="other"} 0`,
		`emqxload_connack_refusals_total{code="5"} 1`, `emqxload_connack_refusals_total{code="135"} 3`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Fatalf("missing %s in\n%s", want, body)
		}
	}
	if strings.Contains(body, `connack_refusals_total{code="0"}`) {
		t.Fatal("zero code exported")
	}
	if strings.Index(body, `stage="dns"`) > strings.Index(body, `stage="tcp"`) {
		t.Fatal("stages not in stable order")
	}
}

func TestStreamIncludesConnectFailures(t *testing.T) {
	m, h := setup(t)
	snap := metrics.Snapshot{ConnectErrors: 3, ConnectFailures: map[string]uint64{"tls_verify_hostname": 3}, ConnackReasons: map[string]uint64{"135": 1}}
	if err := m.persist(Test{ID: "stream-run", Name: "s", Status: "completed", Snapshot: snap}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/tests/stream-run/stream", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, want := range []string{`"connectFailures":{"tls_verify_hostname":3}`, `"connackReasons":{"135":1}`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, w.Body.String())
		}
	}
}

func TestConnectFailuresCSV(t *testing.T) {
	if got := connectFailuresCSV(map[string]uint64{"timeout": 1, "tcp": 4, "dns": 0}); got != "tcp=4;timeout=1" {
		t.Fatal(got)
	}
	if got := connectFailuresCSV(nil); got != "" {
		t.Fatal(got)
	}
}
