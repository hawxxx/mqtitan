package scenario

import (
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
	s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"s1/"}}, "c-${sequence}", pubTopic("s1/fleet/${clientId}"))
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), `workload "pub"`) || !strings.Contains(err.Error(), `forbidden prefix "s1/"`) {
		t.Fatalf("got %v", err)
	}
	s = safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"s1/"}}, "c-${sequence}", pubTopic("loadtest/${clientId}"))
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestForbiddenPrefixHashSuffixNormalised(t *testing.T) {
	s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"s1/#"}}, "c-${sequence}", pubTopic("s1/fleet/x"))
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "forbidden prefix") {
		t.Fatalf("got %v", err)
	}
}

func TestForbiddenPrefixWildcardFilters(t *testing.T) {
	for _, f := range []string{"#", "+/fleet/x", "s1/+/#", "$share/g/s1/fleet/a"} {
		s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"s1/fleet/"}}, "c-${sequence}", subTopic(f))
		if err := s.Validate(); err == nil {
			t.Fatalf("%q accepted", f)
		}
	}
	for _, f := range []string{"s2/#", "s1/other/#"} {
		s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"s1/fleet/"}}, "c-${sequence}", subTopic(f))
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
		{"#", "s1/fleet/", true},
		{"s1/#", "s1/fleet/", true},
		{"s1/fleet/#", "s1/fleet/", true},
		{"s1/fleet/a", "s1/fleet/", true},
		{"s1/+", "s1/fleet/", false},
		{"s1/+/x", "s1/fleet/", true},
		{"+/fleet/x", "s1/fleet/", true},
		{"s1", "s1/fleet/", false},
		{"s1/other/#", "s1/fleet/", false},
		{"s2/#", "s1/fleet/", false},
		{"$share/g/s1/fleet/a", "s1/fleet/", true},
		{"$share/g/s2/a", "s1/fleet/", false},
	} {
		if got := filterOverlapsPrefix(tt.filter, tt.prefix); got != tt.want {
			t.Errorf("filterOverlapsPrefix(%q, %q) = %v, want %v", tt.filter, tt.prefix, got, tt.want)
		}
	}
}

func TestForbiddenPrefixRenderedTopic(t *testing.T) {
	s := safetyScenario(Safety{ForbiddenTopicPrefixes: []string{"s1/"}}, "s1/fleet/${sequence}", pubTopic("${clientId}/x"))
	err := s.Validate()
	if err == nil || !strings.Contains(err.Error(), `forbidden prefix "s1/"`) {
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
	s.Safety.ForbiddenTopicPrefixes = []string{"s1/"}
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
	s := safetyScenario(Safety{ClientIDDenyRegex: `^[0-9a-fA-F]{12}$`, ForbiddenTopicPrefixes: []string{"s1/"}}, "mqtitan-${sequence:08}", pubTopic("loadtest/${clientId}"))
	s.Clients.Count = 1_000_000
	s.Stages[0].TargetClients = 1_000_000
	s.Workloads[0].Clients = 1_000_000
	start := time.Now()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
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
