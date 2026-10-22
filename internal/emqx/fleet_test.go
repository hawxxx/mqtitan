package emqx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

func nodesServer(t *testing.T, live string, status int) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/nodes":
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			_, _ = w.Write([]byte(`[{"node":"emqx@a","node_status":"running","live_connections":` + live + `}]`))
		case "/api/v5/metrics":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

var testCreds = Credentials{APIKey: "key-secret-id", APISecret: "very-secret"}

func TestFetchFleetEMQXTargets(t *testing.T) {
	tel := scenario.Telemetry{}
	for i, live := range []string{"10", "20", "30"} {
		s := nodesServer(t, live, 200)
		tel.EMQX = append(tel.EMQX, scenario.EMQXTarget{Name: "emqx-" + string(rune('0'+i)), Zone: "zone-" + string(rune('a'+i)), URL: s.URL})
	}
	f := FetchFleet(t.Context(), tel, testCreds, nil)
	if f.Source != "emqx" || !f.Complete || len(f.Targets) != 3 {
		t.Fatalf("%+v", f)
	}
	for i, want := range []float64{10, 20, 30} {
		g := f.Targets[i]
		if g.Connections != want || g.Raw != want || g.Error != "" || g.Target != tel.EMQX[i].Name || g.Zone != tel.EMQX[i].Zone {
			t.Fatalf("target %d: %+v", i, g)
		}
	}
}

func TestFetchFleetPartialFailure(t *testing.T) {
	good, bad := nodesServer(t, "5", 200), nodesServer(t, "0", 500)
	tel := scenario.Telemetry{EMQX: []scenario.EMQXTarget{{Name: "a", URL: good.URL}, {Name: "b", URL: bad.URL}}}
	f := FetchFleet(t.Context(), tel, testCreds, nil)
	if f.Complete || f.Targets[0].Error != "" || f.Targets[1].Error == "" {
		t.Fatalf("%+v", f)
	}
	if strings.Contains(f.Targets[1].Error, "key-secret-id") || strings.Contains(f.Targets[1].Error, "very-secret") {
		t.Fatal("error leaks credentials")
	}
}

func TestFetchFleetExpectedTargetsEMQX(t *testing.T) {
	s := nodesServer(t, "5", 200)
	tel := scenario.Telemetry{ExpectedTargets: 2, EMQX: []scenario.EMQXTarget{{Name: "a", URL: s.URL}}}
	if f := FetchFleet(t.Context(), tel, testCreds, nil); f.Complete || !strings.Contains(f.Error, "1 of 2 expected targets") {
		t.Fatalf("%+v", f)
	}
}

func TestFetchFleetConcurrencyBound(t *testing.T) {
	var cur, peak atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		defer cur.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`[{"node":"n","live_connections":1}]`))
	}))
	defer s.Close()
	tel := scenario.Telemetry{}
	for i := 0; i < 64; i++ {
		tel.EMQX = append(tel.EMQX, scenario.EMQXTarget{Name: "t" + string(rune('A'+i%26)) + string(rune('a'+i/26)), URL: s.URL})
	}
	f := FetchFleet(t.Context(), tel, testCreds, nil)
	if len(f.Targets) != 64 {
		t.Fatalf("targets %d", len(f.Targets))
	}
	if p := peak.Load(); p > 16 || p < 2 {
		t.Fatalf("peak concurrency %d", p)
	}
}

func TestFetchFleetDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redir.Close()
	f := FetchFleet(t.Context(), scenario.Telemetry{EMQX: []scenario.EMQXTarget{{Name: "a", URL: redir.URL}}}, testCreds, nil)
	if hit.Load() || f.Complete {
		t.Fatalf("followed redirect: %+v", f)
	}
}

func TestBaselineFirst(t *testing.T) {
	first := FleetSample{Source: "emqx", Targets: []TargetSample{{Target: "a", Connections: 100, Raw: 100}, {Target: "b", Connections: 80, Raw: 80}}}
	b := NewBaseline(first)
	got := b.Apply(FleetSample{Targets: []TargetSample{{Target: "a", Connections: 150, Raw: 150}, {Target: "b", Connections: 90, Raw: 90}, {Target: "c", Connections: 40, Raw: 40}}})
	for i, w := range []float64{50, 10, 40} {
		if got.Targets[i].Connections != w {
			t.Fatalf("%d: %+v", i, got.Targets)
		}
	}
	if got.Targets[0].Raw != 150 || got.Targets[2].Raw != 40 {
		t.Fatalf("raw changed: %+v", got.Targets)
	}
	low := b.Apply(FleetSample{Targets: []TargetSample{{Target: "a", Connections: 30, Raw: 30}}})
	if low.Targets[0].Connections != 0 || low.Targets[0].Raw != 30 {
		t.Fatalf("%+v", low.Targets)
	}
}

func TestFleetNeverSendsCredentialsToDisallowedHost(t *testing.T) {
	var hits atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer s.Close()
	c := testCreds
	c.AllowedHosts = []string{"other.example.net"}
	f := FetchFleet(t.Context(), scenario.Telemetry{EMQX: []scenario.EMQXTarget{{Name: "a", URL: s.URL}}}, c, nil)
	p := FetchFleet(t.Context(), promTel(s.URL, 0), c, nil)
	if hits.Load() != 0 || f.Complete || p.Complete {
		t.Fatalf("hits=%d %+v %+v", hits.Load(), f, p)
	}
	for in, want := range map[string]bool{"http://Host.Example.net:9090": true, "http://host.example.net": true, "http://evil.test": false} {
		if _, ok := HostAllowed([]string{"host.example.net"}, in); ok != want {
			t.Errorf("%s: %v", in, ok)
		}
	}
	if _, ok := HostAllowed([]string{"host.example.net:9090"}, "http://host.example.net:80"); ok {
		t.Error("port-specific entry matched another port")
	}
}
