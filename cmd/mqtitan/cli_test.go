package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuickRejectsWrappedQoS(t *testing.T) {
	if err := quick([]string{"--qos", "256", "--duration", "1ms"}); err == nil {
		t.Fatal("qos 256 wrapped into valid qos")
	}
}
func TestQuickRejectsUnknownArguments(t *testing.T) {
	if err := quick([]string{"--duration", "1ms", "unexpected"}); err == nil {
		t.Fatal("silently ignored positional arguments")
	}
}
func TestRemoteListenRequiresAuthentication(t *testing.T) {
	if err := checkListen("0.0.0.0:8080", "", false); err == nil {
		t.Fatal("remote control exposed without authentication")
	}
	if err := checkListen("127.0.0.1:8080", "", false); err != nil {
		t.Fatal(err)
	}
}
func TestSplitWorkersTrimsSpacesAndEmptyEntries(t *testing.T) {
	if got := splitWorkers(" worker-a, worker-b ,,"); !reflect.DeepEqual(got, []string{"worker-a", "worker-b"}) {
		t.Fatalf("got %q", got)
	}
	if got := splitWorkers(""); got != nil {
		t.Fatalf("got %q", got)
	}
}
func TestRunAcceptsFlagsBeforeScenario(t *testing.T) {
	err := runScenario([]string{"--controller", "http://127.0.0.1:1", "missing.yaml"})
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("scenario path not taken after flags: %v", err)
	}
	if err := runScenario(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing usage: %v", err)
	}
}
func TestRequestReportsControllerErrorMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_SCENARIO","message":"clients.count must be positive"}}`))
	}))
	defer srv.Close()
	err := request(context.Background(), srv.URL, "POST", "/api/v1/tests", map[string]string{}, nil)
	if err == nil || err.Error() != "controller returned HTTP 400: clients.count must be positive" {
		t.Fatalf("got %v", err)
	}
}
func TestInterruptDuringPollStopsRemoteTest(t *testing.T) {
	polled := make(chan struct{}, 1)
	var stopped atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/tests":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"data":{"id":"t1","status":"running"}}`))
		case r.Method == "GET":
			select {
			case polled <- struct{}{}:
			default:
			}
			<-r.Context().Done()
		case r.URL.Path == "/api/v1/tests/t1/stop":
			stopped.Store(true)
			w.WriteHeader(202)
			_, _ = w.Write([]byte(`{"data":{"status":"stopping"}}`))
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-polled:
		case <-time.After(5 * time.Second):
		}
		cancel()
	}()
	err := remoteRun(ctx, srv.URL, "scenario", nil)
	var exit exitError
	if !errors.As(err, &exit) || exit.code != 130 {
		t.Fatalf("got %v", err)
	}
	if !stopped.Load() {
		t.Fatal("interrupted run left the remote test running")
	}
}

func TestRequestReportsTransportCause(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	err = request(context.Background(), "http://"+address, "GET", "/api/v1/tests", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "controller request failed: ") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error = %v, want transport cause", err)
	}
}
