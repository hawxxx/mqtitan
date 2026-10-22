package scenario

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// safetyScenario builds a valid scenario with the given safety block.
func safetyScenario(safety Safety, idTemplate string, wl ...Workload) Scenario {
	if len(wl) == 0 {
		wl = []Workload{{Name: "pub", Type: "publisher", Clients: 2, Topic: "loadtest/${clientId}", RatePerClient: 1, Payload: Payload{Type: "static", Value: "x"}}}
	}
	return Scenario{
		APIVersion: "mqtitan.io/v1alpha1", Kind: "Scenario", Name: "safety",
		Broker:    Broker{URL: "mqtt://localhost:1883", Version: "3.1.1"},
		Clients:   Clients{Count: 2, IDTemplate: idTemplate},
		Stages:    []Stage{{Duration: Duration{time.Second}, TargetClients: 2}},
		Workloads: wl, Safety: safety,
	}
}

func pubTopic(topic string) Workload {
	return Workload{Name: "pub", Type: "publisher", Clients: 2, Topic: topic, RatePerClient: 1, Payload: Payload{Type: "static", Value: "x"}}
}
func subTopic(topic string) Workload {
	return Workload{Name: "sub", Type: "subscriber", Clients: 2, Topic: topic}
}

func TestForbiddenPrefixPublisher(t *testing.T) {
	s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"devices/"}}, "c-${sequence}", pubTopic("devices/fleet/${clientId}"))
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), `workload "pub"`) || !strings.Contains(err.Error(), `forbidden prefix "devices/"`) {
		t.Fatalf("got %v", err)
	}
	s = safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"devices/"}}, "c-${sequence}", pubTopic("loadtest/${clientId}"))
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestForbiddenPrefixHashSuffixNormalised(t *testing.T) {
	s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"devices/#"}}, "c-${sequence}", pubTopic("devices/fleet/x"))
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "forbidden prefix") {
		t.Fatalf("got %v", err)
	}
}

func TestForbiddenPrefixWildcardFilters(t *testing.T) {
	for _, f := range []string{"#", "+/fleet/x", "devices/+/#", "$share/g/devices/fleet/a", "$queue/devices/fleet/#"} {
		s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"devices/fleet/"}}, "c-${sequence}", subTopic(f))
		if err := s.Validate(); err == nil {
			t.Fatalf("%q accepted", f)
		}
	}
	for _, f := range []string{"ops/#", "devices/other/#"} {
		s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"devices/fleet/"}}, "c-${sequence}", subTopic(f))
		if err := s.Validate(); err != nil {
			t.Fatalf("%q rejected: %v", f, err)
		}
	}
}

func TestFilterOverlapsPrefix(t *testing.T) {
	for _, tt := range []struct {
		filter, prefix string
		want           bool
	}{
		{"#", "devices/fleet/", true},
		{"devices/#", "devices/fleet/", true},
		{"devices/fleet/#", "devices/fleet/", true},
		{"devices/fleet/a", "devices/fleet/", true},
		{"devices/+", "devices/fleet/", false},
		{"devices/+/x", "devices/fleet/", true},
		{"+/fleet/x", "devices/fleet/", true},
		{"devices", "devices/fleet/", false},
		{"devices/other/#", "devices/fleet/", false},
		{"ops/#", "devices/fleet/", false},
		{"$share/g/devices/fleet/a", "devices/fleet/", true},
		{"$share/g/ops/a", "devices/fleet/", false},
		{"$queue/devices/fleet/#", "devices/fleet/", true},
		{"$queue/ops/#", "devices/fleet/", false},
	} {
		if got := filterOverlapsPrefix(tt.filter, tt.prefix); got != tt.want {
			t.Errorf("filterOverlapsPrefix(%q, %q) = %v, want %v", tt.filter, tt.prefix, got, tt.want)
		}
	}
}

func TestForbiddenPrefixRenderedTopic(t *testing.T) {
	s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"devices/"}}, "devices/fleet/${sequence}", pubTopic("${clientId}/x"))
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), `forbidden prefix "devices/"`) {
		t.Fatalf("got %v", err)
	}
}

func TestClientIDDenyRegex(t *testing.T) {
	deny := Safety{ClientIDDenyRegex: `^[0-9a-fA-F]{12}$|^[A-Z0-9]{10}$`}
	err := safetyScenario(deny, "${sequence:012}").Validate()
	if err == nil || !strings.Contains(err.Error(), `client ID "000000000001" matches safety.clientIdDenyRegex`) {
		t.Fatalf("got %v", err)
	}
	if err := safetyScenario(deny, "mqtitan-${sequence:08}").Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClientIDDenyRegexInvalid(t *testing.T) {
	err := safetyScenario(Safety{ClientIDDenyRegex: "(["}, "c-${sequence}").Validate()
	if err == nil || !strings.Contains(err.Error(), "safety.clientIdDenyRegex is not a valid regular expression") {
		t.Fatalf("got %v", err)
	}
}

func TestCheckRenderedUsesWorker(t *testing.T) {
	s := safetyScenario(Safety{ClientIDDenyRegex: `^abc-`}, "${worker}-${sequence}")
	s.Clients.Worker = "abc"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), `"abc-1"`) {
		t.Fatalf("got %v", err)
	}
	s = safetyScenario(Safety{ClientIDDenyRegex: `^[0-9a-fA-F]{12}$`}, "${worker}${sequence:012}")
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), `"000000000001"`) {
		t.Fatalf("empty worker must render empty: %v", err)
	}
}

func TestCheckRenderedSkippedWithoutRules(t *testing.T) {
	before := renderCount.Load()
	s := safetyScenario(Safety{}, "c-${sequence}")
	s.Clients.Count = 1_000_000
	s.Stages[0].TargetClients = 1_000_000
	s.Workloads[0].Clients = 1_000_000
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	// prefixes without templated topics need no rendering either
	s.Safety.ForbiddenTopicPrefixes = []string{"devices/"}
	s.Workloads[0].Topic = "loadtest/static"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if n := renderCount.Load() - before; n != 0 {
		t.Fatalf("rendered %d IDs", n)
	}
}

func TestRenderCheckBound(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	s := safetyScenario(Safety{ClientIDDenyRegex: `^[0-9a-fA-F]{12}$`, ForbiddenTopicPrefixes: []string{"devices/"}}, "mqtitan-${sequence:08}", pubTopic("loadtest/${clientId}"))
	s.Clients.Count = 1_000_000
	s.Stages[0].TargetClients = 1_000_000
	s.Workloads[0].Clients = 1_000_000
	start := time.Now()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	limit := 2 * time.Second
	if raceEnabled {
		limit = 10 * time.Second
	}
	if d := time.Since(start); d > limit {
		t.Fatalf("render check took %s", d)
	}
}

func idleScenario(keepAlive, hint time.Duration, w Workload) Scenario {
	s := safetyScenario(Safety{}, "c-${sequence}", w)
	s.Broker.KeepAlive = Duration{keepAlive}
	s.Broker.IdleTimeoutHint = Duration{hint}
	return s
}

func TestIdleTimeoutHintWarning(t *testing.T) {
	conn := Workload{Name: "idle", Type: "connection", Clients: 2}
	sub := Workload{Name: "sub", Type: "subscriber", Clients: 2, Topic: "t/#"}
	pub := func(rate float64) Workload {
		return Workload{Name: "pub", Type: "publisher", Clients: 2, Topic: "t", RatePerClient: rate, Payload: Payload{Type: "static", Value: "x"}}
	}
	h := 100 * time.Second
	w := idleScenario(180*time.Second, h, conn).Plan().Warnings
	if len(w) != 1 || !strings.Contains(w[0], "keepAlive 3m0s >= broker.idleTimeoutHint 1m40s") || !strings.Contains(w[0], "keepAlive <= 50s") {
		t.Fatalf("got %q", w)
	}
	for name, s := range map[string]Scenario{
		"keepalive 60s":  idleScenario(60*time.Second, h, conn),
		"default 30s":    idleScenario(0, h, conn),
		"fast publisher": idleScenario(120*time.Second, h, pub(1)),
		"no hint":        idleScenario(180*time.Second, 0, conn),
	} {
		if w := s.Plan().Warnings; len(w) != 0 {
			t.Errorf("%s: %q", name, w)
		}
	}
	for name, s := range map[string]Scenario{
		"subscriber":     idleScenario(120*time.Second, h, sub),
		"slow publisher": idleScenario(120*time.Second, h, pub(0.005)),
	} {
		if w := s.Plan().Warnings; len(w) != 1 {
			t.Errorf("%s: %q", name, w)
		}
	}
	err := idleScenario(30*time.Second, -time.Second, conn).Validate()
	if err == nil || !strings.Contains(err.Error(), "broker.idleTimeoutHint must be nonnegative") {
		t.Fatalf("got %v", err)
	}
}

func TestSourcePortWarning(t *testing.T) {
	big := func(n int) Scenario {
		s := safetyScenario(Safety{}, "c-${sequence}", Workload{Name: "idle", Type: "connection", Clients: n})
		s.Clients.Count = n
		s.Stages[0].TargetClients = n
		return s
	}
	s := big(60000)
	w := s.Plan().Warnings
	if len(w) != 1 || !strings.Contains(w[0], "60000 clients per worker exceed ~25000 source ports x 1 destination address") {
		t.Fatalf("got %q", w)
	}
	s.Broker.AddressLabels = []AddressLabel{{"192.0.2.0/26", "a"}, {"192.0.2.64/26", "b"}, {"192.0.2.128/26", "c"}}
	if w := s.Plan().Warnings; len(w) != 0 {
		t.Fatalf("labels: %q", w)
	}
	if w := s.PlanWith(PlanOptions{Destinations: 1}).Warnings; len(w) != 1 {
		t.Fatalf("override: %q", w)
	}
	s = big(100000) // brief says 120000, which exceeds 2 x 25000 per worker
	if w := s.PlanWith(PlanOptions{Workers: 2, Destinations: 2}).Warnings; len(w) != 0 {
		t.Fatalf("workers: %q", w)
	}
}

func TestRendererMatchesEngineExpansion(t *testing.T) {
	for _, tpl := range []string{"c-${sequence}", "${worker}-${sequence:012}-${clientId}", "a/${clientId}/${worker}/${sequence:03}/x", "plain", "${sequence:2}${sequence}"} {
		for _, worker := range []string{"", "w1"} {
			r := compileRenderer(tpl, worker)
			for _, seq := range []int{1, 42, 99999} {
				want := strings.ReplaceAll(Expand(tpl, seq, "ID"), "${worker}", worker)
				if got := string(r.appendTo(nil, seq, "ID")); got != want {
					t.Errorf("%q worker %q seq %d: got %q want %q", tpl, worker, seq, got, want)
				}
			}
		}
	}
}

func TestForbiddenPrefixMustBeLiteral(t *testing.T) {
	for _, p := range []string{"devices/+/telemetry/", "devices/#/x", "+", "", "#", "devices/\x00", "devices/a#"} {
		err := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{p}}, "c-${sequence}").Validate()
		if err == nil || !strings.Contains(err.Error(), "safety.forbiddenTopicPrefixes[0] must be a literal topic prefix (wildcards are not allowed)") {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range []string{"devices/", "devices/#", "devices"} {
		if err := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{p}}, "c-${sequence}").Validate(); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestTelemetryValidation(t *testing.T) {
	emqx := []EMQXTarget{{Name: "a", Zone: "z1", URL: "http://198.51.100.1:18083"}}
	prom := &PrometheusSource{URL: "http://prometheus.example.net:9090", Query: "up"}
	cases := []struct {
		name string
		tel  Telemetry
		want string
	}{
		{"ok emqx", Telemetry{EMQX: emqx}, ""},
		{"ok prometheus", Telemetry{Prometheus: prom, Baseline: "first", ExpectedTargets: 4096, Interval: Duration{5 * time.Second}}, ""},
		{"no sources", Telemetry{}, "telemetry must set exactly one of emqx or prometheus"},
		{"both", Telemetry{EMQX: emqx, Prometheus: prom}, "telemetry must set exactly one of emqx or prometheus"},
		{"userinfo", Telemetry{EMQX: []EMQXTarget{{Name: "a", URL: "http://u:p@host"}}}, "credentials must not be in YAML"},
		{"prom userinfo", Telemetry{Prometheus: &PrometheusSource{URL: "http://u:p@host", Query: "up"}}, "credentials must not be in YAML"},
		{"interval", Telemetry{EMQX: emqx, Interval: Duration{time.Second}}, "telemetry.interval must be at least 5s"},
		{"duplicate", Telemetry{EMQX: append(append([]EMQXTarget{}, emqx...), emqx...)}, "duplicates"},
		{"negative expected", Telemetry{EMQX: emqx, ExpectedTargets: -1}, "telemetry.expectedTargets must be between 0 and 4096"},
		{"large expected", Telemetry{EMQX: emqx, ExpectedTargets: 5000}, "telemetry.expectedTargets must be between 0 and 4096"},
		{"baseline", Telemetry{EMQX: emqx, Baseline: "last"}, "telemetry.baseline must be none or first"},
		{"empty query", Telemetry{Prometheus: &PrometheusSource{URL: "http://h"}}, "telemetry.prometheus.query is required"},
	}
	for _, tc := range cases {
		s := safetyScenario(Safety{}, "c-${sequence}")
		tel := tc.tel
		s.Telemetry = &tel
		err := s.Validate()
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: got %v want %q", tc.name, err, tc.want)
		}
	}
	many := make([]EMQXTarget, 1025)
	for i := range many {
		many[i] = EMQXTarget{Name: fmt.Sprint("n", i), URL: "http://h"}
	}
	s := safetyScenario(Safety{}, "c-${sequence}")
	s.Telemetry = &Telemetry{EMQX: many}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "at most 1024") {
		t.Errorf("many: %v", err)
	}
}

func TestBalanceThresholdsRequireTelemetry(t *testing.T) {
	want := "thresholds broker_connection_cv and zone_connection_skew require a telemetry block"
	for _, name := range []string{"broker_connection_cv", "zone_connection_skew"} {
		s := safetyScenario(Safety{}, "c-${sequence}")
		s.Thresholds = map[string]string{name: "<10%"}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
		s.Telemetry = &Telemetry{EMQX: []EMQXTarget{{Name: "a", URL: "http://198.51.100.1:18083"}}}
		if err := s.Validate(); err != nil {
			t.Errorf("%s with telemetry: %v", name, err)
		}
	}
}

func TestPrometheusWithoutExpectedTargetsWarns(t *testing.T) {
	want := "telemetry.expectedTargets is 0; a pod missing from Prometheus will not fail balance thresholds"
	has := func(s Scenario) bool {
		for _, w := range s.Plan().Warnings {
			if w == want {
				return true
			}
		}
		return false
	}
	s := safetyScenario(Safety{}, "c-${sequence}")
	s.Telemetry = &Telemetry{Prometheus: &PrometheusSource{URL: "http://prometheus.example.net:9090", Query: "up"}}
	if has(s) {
		t.Error("warned without a balance threshold")
	}
	s.Thresholds = map[string]string{"zone_connection_skew": "<15%"}
	if !has(s) {
		t.Errorf("no warning: %v", s.Plan().Warnings)
	}
	s.Telemetry.ExpectedTargets = 3
	if has(s) {
		t.Error("warned with expectedTargets set")
	}
	s.Telemetry = &Telemetry{EMQX: []EMQXTarget{{Name: "a", URL: "http://198.51.100.1:18083"}}}
	if has(s) {
		t.Error("warned for an emqx target list")
	}
}
