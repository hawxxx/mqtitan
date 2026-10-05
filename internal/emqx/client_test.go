package emqx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchWhitelistsMetricsAndPreservesMissingValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "key" || pass != "secret" {
			t.Error("API credentials not sent correctly")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v5/nodes":
			w.Write([]byte(`[{"node":"emqx@one","node_status":"running","live_connections":12,"cpu_use":0.25,"memory_used":"32M"},{"node":"emqx@two","live_connections":8}]`))
		case "/api/v5/metrics":
			if r.URL.Query().Get("aggregate") != "false" {
				t.Error("must explicitly request per-node metrics")
			}
			w.Write([]byte(`[{"node":"emqx@one","messages.received":5,"messages.sent":4,"messages.dropped":1,"arbitrary.secret":123},{"node":"emqx@two","messages.received":3,"messages.sent":2,"messages.dropped":0}]`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	s, err := (&Client{BaseURL: server.URL, APIKey: "key", APISecret: "secret"}).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes) != 2 || s.Connections == nil || *s.Connections != 20 || s.MessagesReceived == nil || *s.MessagesReceived != 8 || s.MessagesSent == nil || *s.MessagesSent != 6 || s.MessagesDropped == nil || *s.MessagesDropped != 1 {
		t.Fatalf("wrong snapshot: %+v", s)
	}
	if _, ok := s.Metrics["arbitrary.secret"]; ok {
		t.Fatal("unapproved metric exposed")
	}
	if s.Nodes[1].CPUUse != nil || s.Nodes[0].MemoryUsed != "32M" {
		t.Fatal("missing/raw values changed")
	}
}

func TestFetchRejectsUntrustedTLSAndOversizedBodies(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat("x", 100))) }))
	defer server.Close()
	c := &Client{BaseURL: server.URL, APIKey: "key", APISecret: "secret", MaxResponseBytes: 16}
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
	c.HTTPClient = server.Client()
	if _, err := c.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "response too large") {
		t.Fatalf("want size limit: %v", err)
	}
}

func TestFetchDoesNotExposeAPIErrorBodyOrInventMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/nodes" {
			w.Write([]byte(`[{"node":"emqx@one"}]`))
			return
		}
		w.WriteHeader(401)
		w.Write([]byte("secret-credentials"))
	}))
	defer server.Close()
	s, err := (&Client{BaseURL: server.URL, APIKey: "key", APISecret: "secret-credentials"}).Fetch(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-credentials") {
		t.Fatalf("unsafe error: %v", err)
	}
	if s.Connections != nil || s.MessagesDropped != nil {
		t.Fatal("missing metrics invented")
	}
}

func TestPollContinuesFailuresAndHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	err := (&Client{BaseURL: server.URL, APIKey: "key", APISecret: "secret"}).Poll(ctx, time.Millisecond, func(s Snapshot, err error) {
		if err == nil {
			t.Error("expected API error")
		}
		calls++
		if calls == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestFetchCancelsBlockedRequestsAndRejectsRedirects(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	c := &Client{BaseURL: server.URL, APIKey: "key", APISecret: "secret"}
	if _, err := c.Fetch(context.Background()); err == nil {
		t.Fatal("redirect accepted")
	}
	server.Close()
	if redirected {
		t.Fatal("credentials risked on redirected destination")
	}
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer blocked.Close()
	c.BaseURL = blocked.URL
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request not canceled: %v", err)
	}
}
