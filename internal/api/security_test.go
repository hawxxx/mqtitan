package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func call(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWorkerTokenIsSeparatedFromControlToken(t *testing.T) {
	m, _ := setup(t)
	h := Handler(m, Options{Token: "control", WorkerToken: "worker"})
	register := `{"id":"w1"}`
	if got := call(h, "POST", "/api/v1/workers/register", "control", register).Code; got != 401 {
		t.Fatalf("control token on worker route = %d, want 401", got)
	}
	if got := call(h, "POST", "/api/v1/workers/register", "worker", register).Code; got != 201 {
		t.Fatalf("worker token on worker route = %d, want 201", got)
	}
	if got := call(h, "GET", "/api/v1/tests", "worker", "").Code; got != 403 {
		t.Fatalf("worker token on control route = %d, want 403", got)
	}
	if got := call(h, "GET", "/api/v1/tests", "control", "").Code; got != 200 {
		t.Fatalf("control token on control route = %d, want 200", got)
	}
}

func TestResponsesCarryContentSecurityPolicy(t *testing.T) {
	_, h := setup(t)
	w := call(h, "GET", "/healthz", "", "")
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP = %q", csp)
	}
}

func TestAPIRejectsTLSFilePathsButLocalCLIAcceptsThem(t *testing.T) {
	m, h := setup(t)
	raw := strings.Replace(testScenario, "mqtt://127.0.0.1:1", "mqtts://127.0.0.1:1", 1)
	raw = strings.Replace(raw, "  cleanStart: true\n", "  cleanStart: true\n  tls:\n    caFile: /etc/hostname\n", 1)
	for _, path := range []string{"/api/v1/plan", "/api/v1/tests"} {
		body := `{"scenario":` + jsonString(raw) + `}`
		if w := call(h, "POST", path, "controller-secret", body); w.Code != 400 || !strings.Contains(w.Body.String(), "caFile") {
			t.Fatalf("%s accepted a TLS file path: %d %s", path, w.Code, w.Body.String())
		}
	}
	if _, err := m.StartLocal(raw, nil); err != nil && strings.Contains(err.Error(), "caFile") {
		t.Fatalf("local CLI run refused a TLS file path: %v", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
