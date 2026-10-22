package api

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/platform"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Options struct {
	Token string
	// WorkerToken, when set, is the only credential accepted on the worker
	// registration and heartbeat routes, and is rejected everywhere else.
	// Assignments carry resolved broker credentials, so a leaked worker
	// token must not grant control and the control token must not pose as
	// a worker.
	WorkerToken string
	WebDir      string
	WebFS       fs.FS
	Authorize   func(*http.Request) bool
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func input(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		// Workers compress reports: dense latency histograms are mostly zeros.
		// Bound the decompressed size as well as the compressed body.
		z, err := gzip.NewReader(r.Body)
		if err != nil {
			writeError(w, 400, "INVALID_REQUEST", "invalid gzip body")
			return false
		}
		defer z.Close()
		body = &io.LimitedReader{R: z, N: 8 << 20}
	}
	d := json.NewDecoder(body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid request body")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "INVALID_REQUEST", "expected one JSON value")
		return false
	}
	return true
}
func resultError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, 404, "NOT_FOUND", "resource not found")
	} else {
		writeError(w, 500, "INTERNAL_ERROR", "operation failed")
	}
}

func Handler(m *Manager, opts Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ready"}) })
	mux.HandleFunc("GET /api/v1/tests", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.List()
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/v1/tests", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Scenario   string   `json:"scenario"`
			ScenarioID string   `json:"scenarioId"`
			Workers    []string `json:"workers,omitempty"`
		}
		if !input(w, r, &body) {
			return
		}
		if body.ScenarioID != "" {
			raw, err := m.RawScenario(body.ScenarioID)
			if err != nil {
				resultError(w, err)
				return
			}
			body.Scenario = raw
		}
		v, err := m.StartOnWorkers(body.Scenario, body.Workers)
		if err != nil {
			if errors.Is(err, ErrBusy) {
				writeError(w, 409, "TEST_RUNNING", err.Error())
			} else {
				writeError(w, 400, "INVALID_SCENARIO", err.Error())
			}
			return
		}
		writeJSON(w, 201, v)
	})
	mux.HandleFunc("GET /api/v1/tests/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Get(r.PathValue("id"))
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/v1/tests/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := m.Stop(r.PathValue("id")); err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 202, map[string]string{"status": "stopping"})
	})
	mux.HandleFunc("GET /api/v1/tests/{id}/metrics", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Samples(r.PathValue("id"))
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/tests/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		t, err := m.Get(id)
		if err != nil {
			resultError(w, err)
			return
		}
		f, ok := w.(http.Flusher)
		if !ok {
			writeError(w, 500, "STREAM_UNAVAILABLE", "streaming unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		send := func(kind string, v any) bool {
			b, _ := json.Marshal(v)
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
			f.Flush()
			return err == nil
		}
		if t.Status != "running" {
			send("status", t)
			return
		}
		ch, unsub, err := m.Subscribe(id)
		if err != nil {
			// The run may have finished between Get and Subscribe; report its final state.
			if t, err = m.Get(id); err == nil {
				send("status", t)
			}
			return
		}
		defer unsub()
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-ch:
				if !send(e.Kind, e.Data) {
					return
				}
				if e.Kind == "status" {
					if v, ok := e.Data.(Test); ok && v.Status != "running" {
						return
					}
				}
			case <-tick.C:
				if !send("heartbeat", map[string]string{"time": time.Now().UTC().Format(time.RFC3339)}) {
					return
				}
			}
		}
	})
	mux.HandleFunc("GET /api/v1/scenarios", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Scenarios()
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/v1/scenarios", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
			YAML string `json:"yaml"`
		}
		if !input(w, r, &body) {
			return
		}
		v, err := m.SaveScenario(body.Name, body.YAML)
		if err != nil {
			writeError(w, 400, "INVALID_SCENARIO", err.Error())
			return
		}
		writeJSON(w, 201, v)
	})
	mux.HandleFunc("POST /api/v1/plan", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Scenario string `json:"scenario"`
			Workers  *int   `json:"workers"`
		}
		if !input(w, r, &body) {
			return
		}
		workers := 1
		if body.Workers != nil {
			workers = *body.Workers
		}
		if workers < 1 || workers > 256 {
			writeError(w, 400, "INVALID_REQUEST", "workers must be between 1 and 256")
			return
		}
		s, err := decodeScenario(body.Scenario)
		if err == nil {
			s, err = m.resolveCertificates(s)
		}
		if err != nil {
			writeError(w, 400, "INVALID_SCENARIO", err.Error())
			return
		}
		p := s.PlanWith(scenario.PlanOptions{Workers: workers})
		checks := platform.AssessPlan(platform.PlanInputs{Clients: int64(p.PeakClients), FileDescriptors: int64(p.EstimatedFileDescriptors), MemoryBytes: p.EstimatedMemoryBytes, WireBytesPerSecond: p.EstimatedWireBytesPerSecond}, platform.CurrentCapacity())
		writeJSON(w, 200, struct {
			scenario.Plan
			Checks []platform.Check `json:"checks"`
		}{p, checks})
	})
	mux.HandleFunc("GET /api/v1/doctor", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, platform.Doctor()) })
	mux.HandleFunc("GET /api/v1/tests/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		t, err := m.Get(id)
		if err != nil {
			resultError(w, err)
			return
		}
		samples, err := m.Samples(id)
		if err != nil {
			resultError(w, err)
			return
		}
		format := r.URL.Query().Get("format")
		if format == "" {
			format = "json"
		}
		if format != "csv" && format != "json" {
			writeError(w, 400, "INVALID_FORMAT", "format must be json or csv")
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="mqtitan-`+t.ID+`.`+format+`"`)
		if format == "json" {
			writeJSON(w, 200, map[string]any{"test": t, "samples": samples})
			return
		}
		w.Header().Set("Content-Type", "text/csv")
		c := csv.NewWriter(w)
		_ = c.Write([]string{"timestamp", "connected", "published", "messages_per_second", "bytes_per_second", "p50_ms", "p95_ms", "p99_ms", "connection_errors", "publish_errors", "received", "end_to_end_p95_ms", "subscribe_errors", "connect_failures"})
		for _, s := range samples {
			_ = c.Write([]string{s.Timestamp.Format(time.RFC3339Nano), strconv.FormatInt(s.Snapshot.Connected, 10), strconv.FormatUint(s.Snapshot.Published, 10), fmt.Sprint(s.MessageRate), fmt.Sprint(s.ByteRate), fmt.Sprint(float64(s.Snapshot.P50) / 1e6), fmt.Sprint(float64(s.Snapshot.P95) / 1e6), fmt.Sprint(float64(s.Snapshot.P99) / 1e6), fmt.Sprint(s.Snapshot.ConnectErrors), fmt.Sprint(s.Snapshot.PublishErrors), strconv.FormatUint(s.Snapshot.Received, 10), fmt.Sprint(float64(s.Snapshot.EndToEndP95) / 1e6), strconv.FormatUint(s.Snapshot.SubscribeErrors, 10), connectFailuresCSV(s.Snapshot.ConnectFailures)})
		}
		c.Flush()
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		m.prometheus(w)
	})
	m.workerRoutes(mux)
	m.certificateRoutes(mux)
	m.loadRoutes(mux)
	m.eventRoutes(mux)
	m.rerunRoutes(mux)
	m.brokerRoute(mux)
	if opts.WebDir != "" || opts.WebFS != nil {
		assets := opts.WebFS
		if assets == nil {
			assets = os.DirFS(opts.WebDir)
		}
		fileServer := http.FileServer(http.FS(assets))
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeError(w, 404, "NOT_FOUND", "route not found")
				return
			}
			clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+r.URL.Path)), "/")
			if _, err := fs.Stat(assets, clean); err == nil && clean != "" {
				fileServer.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(clean, "assets/") {
				http.NotFound(w, r)
				return
			}
			index, err := fs.ReadFile(assets, "index.html")
			if err != nil {
				http.Error(w, "UI assets unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Frame-Options", "DENY")
		if workerRoute(r.URL.Path) && opts.WorkerToken != "" {
			if !bearer(r, opts.WorkerToken) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, 401, "UNAUTHORIZED", "valid worker token required")
				return
			}
			mux.ServeHTTP(w, r)
			return
		}
		if opts.WorkerToken != "" && bearer(r, opts.WorkerToken) && opts.WorkerToken != opts.Token {
			writeError(w, 403, "FORBIDDEN", "worker token is only valid on worker routes")
			return
		}
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics") {
			if opts.Authorize != nil || opts.Token != "" {
				auth := r.Header.Get("Authorization")
				authorized := false
				if opts.Authorize != nil {
					authorized = opts.Authorize(r)
				} else if strings.HasPrefix(auth, "Bearer ") {
					authorized = bearer(r, opts.Token)
				}
				if !authorized {
					w.Header().Set("WWW-Authenticate", "Bearer")
					writeError(w, 401, "UNAUTHORIZED", "valid bearer token required")
					return
				}
			}
			if r.Method != "GET" && r.Method != "HEAD" {
				if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
					writeError(w, 403, "ORIGIN_REJECTED", "cross-origin mutation rejected")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// The dashboard loads its own scripts and Google Fonts; React sets inline
// style attributes, so style-src needs 'unsafe-inline'.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

func workerRoute(path string) bool {
	return path == "/api/v1/workers/register" || path == "/api/v1/workers/heartbeat"
}
func bearer(r *http.Request, token string) bool {
	given, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && token != "" && subtle.ConstantTimeCompare([]byte(given), []byte(token)) == 1
}
func sameOrigin(origin, host string) bool {
	return origin == "http://"+host || origin == "https://"+host
}

// connectFailuresCSV formats non-zero stage counts as stage=n;stage=n in stage order.
func connectFailuresCSV(failures map[string]uint64) string {
	var b strings.Builder
	for _, stage := range metrics.ConnectStages() {
		if n := failures[stage]; n > 0 {
			if b.Len() > 0 {
				b.WriteByte(';')
			}
			b.WriteString(stage + "=" + strconv.FormatUint(n, 10))
		}
	}
	return b.String()
}
