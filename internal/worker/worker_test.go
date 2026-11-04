package worker

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/platform"
)

func TestWorkerRegistersAuthenticatesAndCancels(t *testing.T) {
	var registered atomic.Bool
	var heartbeats atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer worker-secret" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workers/register" {
			registered.Store(true)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"status": "registered"}})
			return
		}
		heartbeats.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"serverTime": time.Now().UTC()}})
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	err := Run(ctx, Options{Controller: s.URL, ID: "worker-a", Token: "worker-secret"})
	if err != context.DeadlineExceeded {
		t.Fatalf("cancellation: %v", err)
	}
	if !registered.Load() || heartbeats.Load() == 0 {
		t.Fatal("worker did not register/heartbeat")
	}
}

func TestWorkerReregistersAfterControllerRestart(t *testing.T) {
	var registrations, beats atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workers/register" {
			registrations.Add(1)
			fmt.Fprint(w, `{"data":{}}`)
			return
		}
		if beats.Add(1) == 1 {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":{"code":"WORKER_UNKNOWN"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"serverTime": time.Now().UTC()}})
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
	defer cancel()
	_ = Run(ctx, Options{Controller: s.URL, ID: "restart-worker"})
	if registrations.Load() < 2 {
		t.Fatalf("worker never recovered registration: %d registrations", registrations.Load())
	}
}

func TestWorkerSendsSessionAndKeepsRegistrationOnRejectedReport(t *testing.T) {
	var registrations, beats atomic.Int32
	var badSession atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/workers/register" {
			registrations.Add(1)
			fmt.Fprint(w, `{"data":{"status":"registered","session":"s3cret"}}`)
			return
		}
		var h struct {
			Session string `json:"session"`
		}
		if z, err := gzip.NewReader(r.Body); err == nil {
			_ = json.NewDecoder(z).Decode(&h)
		}
		if h.Session != "s3cret" {
			badSession.Store(true)
		}
		if beats.Add(1) == 1 {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"error":{"code":"REPORT_REJECTED"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"serverTime": time.Now().UTC()}})
	}))
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2200*time.Millisecond)
	defer cancel()
	_ = Run(ctx, Options{Controller: s.URL, ID: "session-worker"})
	if badSession.Load() {
		t.Fatal("heartbeat omitted the issued session")
	}
	if registrations.Load() != 1 || beats.Load() < 2 {
		t.Fatalf("rejected report forced re-registration: registrations=%d beats=%d", registrations.Load(), beats.Load())
	}
}

func TestMemoryNearLimit(t *testing.T) {
	gib := uint64(1 << 30)
	cases := []struct {
		name string
		used uint64
		host platform.Host
		want bool
	}{
		{"unknown limit never aborts", 100 * gib, platform.Host{}, false},
		{"below threshold", 8 * gib, platform.Host{MemoryLimitBytes: 10 * gib}, false},
		{"at threshold", 9 * gib, platform.Host{MemoryLimitBytes: 10 * gib}, true},
		{"container limit wins over host total", 5 * gib, platform.Host{MemoryLimitBytes: 5 * gib, MemoryTotalBytes: 64 * gib}, true},
		{"falls back to host total", 60 * gib, platform.Host{MemoryTotalBytes: 64 * gib}, true},
	}
	for _, c := range cases {
		if got := memoryNearLimit(c.used, c.host); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
