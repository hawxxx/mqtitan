package api

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
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

func TestGzipBodiesAreDecodedAndBounded(t *testing.T) {
	_, h := setup(t)
	send := func(payload []byte) int {
		var b strings.Builder
		z := gzip.NewWriter(&b)
		_, _ = z.Write(payload)
		_ = z.Close()
		r := httptest.NewRequest("POST", "/api/v1/workers/register", strings.NewReader(b.String()))
		r.Header.Set("Authorization", "Bearer controller-secret")
		r.Header.Set("Content-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := send([]byte(`{"id":"gz"}`)); got != 201 {
		t.Fatalf("gzip registration = %d, want 201", got)
	}
	// 16 MiB of zeros compresses below the 2 MiB body cap; the decompressed
	// stream must still be refused.
	bomb := append([]byte(`{"id":"`), make([]byte, 16<<20)...)
	if got := send(bomb); got != 400 {
		t.Fatalf("decompression bomb = %d, want 400", got)
	}
}

func TestLoopbackOnlyRejectsRebindingHosts(t *testing.T) {
	m, _ := setup(t)
	h := Handler(m, Options{LoopbackOnly: true})
	for host, want := range map[string]int{
		"127.0.0.1:8080":    200,
		"localhost:8080":    200,
		"LOCALHOST":         200,
		"[::1]:8080":        200,
		"evil.example:8080": 403,
		"10.0.0.5:8080":     403,
	} {
		r := httptest.NewRequest("GET", "/api/v1/tests", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("Host %q = %d, want %d", host, w.Code, want)
		}
	}
	r := httptest.NewRequest("GET", "/healthz", nil)
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("health check with foreign Host = %d, want 200", w.Code)
	}
}

func TestCrossOriginMutationRejected(t *testing.T) {
	m, _ := setup(t)
	h := Handler(m, Options{})
	for origin, rejected := range map[string]bool{
		"http://example.com":      false,
		"https://example.com":     false,
		"http://evil.example":     true,
		"http://example.com.evil": true,
	} {
		r := httptest.NewRequest("POST", "/api/v1/plan", strings.NewReader(`{}`))
		r.Host = "example.com"
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := strings.Contains(w.Body.String(), "ORIGIN_REJECTED"); got != rejected {
			t.Fatalf("Origin %q rejected = %v, want %v (%d %s)", origin, got, rejected, w.Code, w.Body.String())
		}
	}
}

func TestAuthRoutesBypassAPIAuthentication(t *testing.T) {
	m, _ := setup(t)
	auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"login": true}) })
	h := Handler(m, Options{Token: "control", AuthRoutes: auth})
	if got := call(h, "GET", "/auth/session", "", "").Code; got != 200 {
		t.Fatalf("auth route without credentials = %d, want 200", got)
	}
	if got := call(h, "GET", "/api/v1/tests", "", "").Code; got != 401 {
		t.Fatalf("API without credentials = %d, want 401", got)
	}
}

func TestAuthRoutesCoexistWithDashboardAssets(t *testing.T) {
	m, _ := setup(t)
	auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"login": true}) })
	assets := fstest.MapFS{"index.html": {Data: []byte("<html></html>")}}
	h := Handler(m, Options{Token: "control", AuthRoutes: auth, WebFS: assets})
	for _, tc := range []struct{ method, path string }{{"GET", "/auth/session"}, {"POST", "/auth/logout"}} {
		if got := call(h, tc.method, tc.path, "", "").Code; got != 200 {
			t.Fatalf("%s %s = %d, want 200", tc.method, tc.path, got)
		}
	}
	if got := call(h, "GET", "/", "", "").Code; got != 200 {
		t.Fatalf("GET / = %d, want 200", got)
	}
}
