package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/emqx"
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

func TestPrometheusDropsInvalidWorkerKeys(t *testing.T) {
	m, h := setup(t)
	snap := metrics.Snapshot{ConnectErrors: 3, ConnectFailures: map[string]uint64{"tcp": 1, "bogus": 2},
		ConnackReasons: map[string]uint64{"5": 1, "-1": 4, "300": 4, "abc": 4}}
	if err := m.persist(Test{ID: "bad-keys", Name: "b", Status: "completed", Snapshot: snap}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `emqxload_connack_refusals_total{code="5"} 1`) {
		t.Fatal(body)
	}
	for _, bad := range []string{`code="-1"`, `code="300"`, `code="abc"`, `bogus`} {
		if strings.Contains(body, bad) {
			t.Fatalf("series %s exported", bad)
		}
	}
}

func addressSnapshot() metrics.Snapshot {
	return metrics.Snapshot{
		RemoteAddresses: []metrics.LabelCount{{Key: "192.0.2.1:1883", Label: "new-nlb-1a", Connects: 7, Active: 5}, {Key: "192.0.2.2:1883", Label: "unlabelled", Connects: 3, Active: 3}, {Key: "other", Connects: 2}},
		AddressLabels:   []metrics.LabelCount{{Key: "new-nlb-1a", Connects: 7, Active: 5}, {Key: "we\"ird\n", Connects: 1, Active: 1}},
		DNSAnswers:      []metrics.LabelCount{{Key: "192.0.2.1", Connects: 4}},
		DNSErrors:       []metrics.LabelCount{{Key: "192.0.2.53:53", Connects: 2}},
	}
}

func TestPrometheusRemoteAddressSeries(t *testing.T) {
	m, h := setup(t)
	old := metrics.Snapshot{RemoteAddresses: []metrics.LabelCount{{Key: "198.51.100.9:1883", Connects: 99, Active: 9}}}
	if err := m.persist(Test{ID: "addr-old", Name: "o", Status: "completed", StartedAt: time.Now().Add(-time.Hour), Snapshot: old}); err != nil {
		t.Fatal(err)
	}
	if err := m.persist(Test{ID: "addr-new", Name: "n", Status: "completed", StartedAt: time.Now(), Snapshot: addressSnapshot()}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{
		`emqxload_remote_connects_total{address="192.0.2.1:1883",label="new-nlb-1a"} 7`,
		`emqxload_remote_active{address="192.0.2.1:1883",label="new-nlb-1a"} 5`,
		`emqxload_remote_connects_total{address="other",label=""} 2`,
		`emqxload_address_label_active{label="new-nlb-1a"} 5`,
		`emqxload_address_label_active{label="we\"ird\n"} 1`,
		`emqxload_dns_answers_total{address="192.0.2.1"} 4`,
		`emqxload_dns_errors_total{resolver="192.0.2.53:53"} 2`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Fatalf("missing %s in\n%s", want, body)
		}
	}
	if strings.Contains(body, "198.51.100.9") {
		t.Fatal("older test exported per-address series")
	}
}

func TestStreamIncludesRemoteAddresses(t *testing.T) {
	m, h := setup(t)
	if err := m.persist(Test{ID: "addr-stream", Name: "s", Status: "completed", Snapshot: addressSnapshot()}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/tests/addr-stream/stream", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, want := range []string{`"remoteAddresses":[{"key":"192.0.2.1:1883","label":"new-nlb-1a","connects":7,"active":5}`, `"addressLabels":`, `"dnsAnswers":`, `"dnsErrors":`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s in %s", want, w.Body.String())
		}
	}
}

func TestPlanEndpointAcceptsWorkers(t *testing.T) {
	_, h := setup(t)
	y := `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: big
broker: {url: 'mqtt://localhost:1883', version: '3.1.1'}
clients: {count: 60000, idTemplate: 'c-${sequence}'}
stages: [{duration: 1s, targetClients: 60000}]
workloads: [{name: idle, type: connection, clients: 60000}]
`
	post := func(workers string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"scenario": y})
		raw := string(body)
		if workers != "" {
			raw = strings.TrimSuffix(raw, "}") + `,"workers":` + workers + "}"
		}
		r := httptest.NewRequest("POST", "/api/v1/plan", strings.NewReader(raw))
		r.Header.Set("Authorization", "Bearer controller-secret")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := post(""); w.Code != 200 || !strings.Contains(w.Body.String(), "source ports") {
		t.Fatalf("default: %d %s", w.Code, w.Body.String())
	}
	if w := post("3"); w.Code != 200 || strings.Contains(w.Body.String(), "source ports") {
		t.Fatalf("workers 3: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{"0", "257", "-1"} {
		if w := post(bad); w.Code != 400 {
			t.Fatalf("workers %s: %d", bad, w.Code)
		}
	}
}

func TestPrometheusLabelEscaping(t *testing.T) {
	in := "a\tb\"c\\d\ne€"
	if got, want := promLabel(in), "a\tb\\\"c\\\\d\\ne€"; got != want {
		t.Fatalf("promLabel(%q) = %q, want %q", in, got, want)
	}
	m, h := setup(t)
	snap := metrics.Snapshot{AddressLabels: []metrics.LabelCount{{Key: in, Active: 1}}, RemoteAddresses: []metrics.LabelCount{{Key: "192.0.2.1:1883", Label: in, Connects: 1}}}
	if err := m.persist(Test{ID: "escape", Name: "e", Status: "completed", StartedAt: time.Now(), Snapshot: snap}); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]emqx.FleetSample{{Complete: true, Targets: []emqx.TargetSample{{Target: in, Zone: in, Connections: 2}}}})
	if err := m.store.Put("brokers", "escape", b); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	esc := promLabel(in)
	for _, want := range []string{
		`emqxload_address_label_active{label="` + esc + `"} 1`,
		`emqxload_remote_connects_total{address="192.0.2.1:1883",label="` + esc + `"} 1`,
		`emqxload_broker_connections{target="` + esc + `",zone="` + esc + `"} 2`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Fatalf("missing %s in\n%s", want, body)
		}
	}
	if strings.Contains(body, `\u20ac`) || strings.Contains(body, `\t`) {
		t.Fatal("Go escapes in label values")
	}
}

func TestPrometheusQuantilesDescribeLatestTest(t *testing.T) {
	m, h := setup(t)
	now := time.Now()
	// IDs sort the older test first, so tests[0] is not the latest by StartedAt.
	for _, x := range []Test{
		{ID: "a-new", Name: "n", Status: "completed", StartedAt: now, Snapshot: metrics.Snapshot{EndToEndP95: time.Second}},
		{ID: "b-old", Name: "o", Status: "completed", StartedAt: now.Add(-time.Hour), Snapshot: metrics.Snapshot{EndToEndP95: 7 * time.Second}},
		{ID: "c-old", Name: "o", Status: "completed", StartedAt: now.Add(-2 * time.Hour), Snapshot: metrics.Snapshot{EndToEndP95: 9 * time.Second}},
	} {
		if err := m.persist(x); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if body := w.Body.String(); !strings.Contains(body, `emqxload_e2e_latency_seconds{quantile="0.95"} 1`+"\n") {
		t.Fatalf("quantiles not from the latest test:\n%s", body)
	}
}
