package emqx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

func promServer(t *testing.T, body string, check func(*http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func vector(series ...string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(series, ",") + `]}}`
}
func series(pod, zone, v string) string {
	m := `"pod":"` + pod + `"`
	if zone != "" {
		m += `,"zone":"` + zone + `"`
	}
	return `{"metric":{` + m + `},"value":[1700000000,"` + v + `"]}`
}
func promTel(url string, expected int) scenario.Telemetry {
	return scenario.Telemetry{ExpectedTargets: expected, Prometheus: &scenario.PrometheusSource{URL: url, Query: "sum by (pod, zone) (up)"}}
}

func TestPrometheusVector(t *testing.T) {
	s := promServer(t, vector(series("emqx-0", "zone-a", "120")), func(r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != "sum by (pod, zone) (up)" {
			t.Errorf("request %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer t" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
	})
	f := FetchFleet(t.Context(), promTel(s.URL, 0), Credentials{PromToken: "t"}, nil)
	if f.Source != "prometheus" || !f.Complete || len(f.Targets) != 1 {
		t.Fatalf("%+v", f)
	}
	if g := f.Targets[0]; g.Target != "emqx-0" || g.Zone != "zone-a" || g.Connections != 120 || g.Raw != 120 {
		t.Fatalf("%+v", g)
	}
}

func TestPrometheusBasicAuth(t *testing.T) {
	s := promServer(t, vector(series("a", "", "1")), func(r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "user" || p != "pw" {
			t.Errorf("basic auth %v", ok)
		}
	})
	f := FetchFleet(t.Context(), promTel(s.URL, 0), Credentials{PromUser: "user", PromPassword: "pw"}, nil)
	if !f.Complete {
		t.Fatalf("%+v", f)
	}
}

func TestPrometheusMissingLabels(t *testing.T) {
	s := promServer(t, vector(series("a", "", "5")), nil)
	f := FetchFleet(t.Context(), promTel(s.URL, 0), Credentials{}, nil)
	if !f.Complete || f.Targets[0].Zone != "" {
		t.Fatalf("%+v", f)
	}
	s = promServer(t, vector(`{"metric":{"zone":"z"},"value":[1,"5"]}`), nil)
	f = FetchFleet(t.Context(), promTel(s.URL, 0), Credentials{}, nil)
	if f.Complete || len(f.Targets) != 1 || f.Targets[0].Error != "missing label pod" {
		t.Fatalf("%+v", f)
	}
}

func TestPrometheusFewerThanExpectedTargets(t *testing.T) {
	for n, complete := range map[int]bool{3: false, 4: true, 5: true} {
		var ss []string
		for i := 0; i < n; i++ {
			ss = append(ss, series("p"+string(rune('0'+i)), "z", "1"))
		}
		s := promServer(t, vector(ss...), nil)
		f := FetchFleet(t.Context(), promTel(s.URL, 4), Credentials{}, nil)
		if f.Complete != complete {
			t.Fatalf("%d series: %+v", n, f)
		}
		if n == 3 && !strings.Contains(f.Error, "3 of 4 expected targets") {
			t.Fatalf("error %q", f.Error)
		}
	}
}

func TestPrometheusErrorStatus(t *testing.T) {
	for _, body := range []string{
		`{"status":"error","errorType":"bad_data","error":"secret-detail"}`,
		`{"status":"success","data":{"resultType":"matrix","result":[]}}`,
		`not json`,
	} {
		s := promServer(t, body, nil)
		f := FetchFleet(t.Context(), promTel(s.URL, 0), Credentials{}, nil)
		if f.Complete || f.Error == "" || strings.Contains(f.Error, "secret-detail") {
			t.Fatalf("%s: %+v", body, f)
		}
	}
}

func TestPrometheusCapsResponseAndRefusesRedirects(t *testing.T) {
	s := promServer(t, vector(series("a", "", "1"))+strings.Repeat(" ", 2<<20), nil)
	if f := FetchFleet(t.Context(), promTel(s.URL, 0), Credentials{}, nil); f.Complete {
		t.Fatal("oversized response accepted")
	}
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, s.URL, 302) }))
	defer redir.Close()
	if f := FetchFleet(t.Context(), promTel(redir.URL, 0), Credentials{}, nil); f.Complete {
		t.Fatal("redirect followed")
	}
}
