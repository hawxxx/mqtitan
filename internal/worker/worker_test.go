package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
