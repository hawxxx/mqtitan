package api

import (
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
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
	Token     string
	WebDir    string
	WebFS     fs.FS
	Authorize func(*http.Request) bool
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
	d := json.NewDecoder(r.Body)
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
		}
		if !input(w, r, &body) {
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
		p := s.Plan()
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
		_ = c.Write([]string{"timestamp", "connected", "published", "messages_per_second", "bytes_per_second", "p50_ms", "p95_ms", "p99_ms", "connection_errors", "publish_errors"})
		for _, s := range samples {
			_ = c.Write([]string{s.Timestamp.Format(time.RFC3339Nano), strconv.FormatInt(s.Snapshot.Connected, 10), strconv.FormatUint(s.Snapshot.Published, 10), fmt.Sprint(s.MessageRate), fmt.Sprint(s.ByteRate), fmt.Sprint(float64(s.Snapshot.P50) / 1e6), fmt.Sprint(float64(s.Snapshot.P95) / 1e6), fmt.Sprint(float64(s.Snapshot.P99) / 1e6), fmt.Sprint(s.Snapshot.ConnectErrors), fmt.Sprint(s.Snapshot.PublishErrors)})
		}
		c.Flush()
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		m.prometheus(w)
	})
	m.workerRoutes(mux)
	m.certificateRoutes(mux)
	m.loadRoutes(mux)
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
		if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && (strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics") {
			if opts.Authorize != nil || opts.Token != "" {
				auth := r.Header.Get("Authorization")
				authorized := false
				if opts.Authorize != nil {
					authorized = opts.Authorize(r)
				} else if strings.HasPrefix(auth, "Bearer ") {
					authorized = subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), []byte(opts.Token)) == 1
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
func sameOrigin(origin, host string) bool {
	return origin == "http://"+host || origin == "https://"+host
}
