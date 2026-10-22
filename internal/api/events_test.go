package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/scenario"
)

func messages(events []LogEvent) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Level + " " + e.Message + "\n")
	}
	return b.String()
}

func TestRunLogRecordsLifecycleAndLoadChanges(t *testing.T) {
	m, h := setup(t)
	run, err := m.Start(strings.Replace(testScenario, "150ms", "5s", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateLoad(run.ID, LoadRequest{Clients: 0, RatePerClient: 0, Revision: 0}); err != nil {
		t.Fatal(err)
	}
	if err = m.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "test end", func() bool { v, _ := m.Get(run.ID); return v.Status != "running" })
	r := httptest.NewRequest("GET", "/api/v1/tests/"+run.ID+"/events", nil)
	r.Header.Set("Authorization", "Bearer controller-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("events: %d", w.Code)
	}
	var body struct{ Data []LogEvent }
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	events := body.Data
	log := messages(events)
	for _, want := range []string{"info Test \"API test\" started on the local engine", "info Stage 1/1: ramp to 1 clients", "action Live load changed: clients 1 → 0", "warn Test stopped"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q in:\n%s", want, log)
		}
	}
	for i, e := range events {
		if e.Seq != i+1 {
			t.Fatalf("sequence gap at %d: %d", i, e.Seq)
		}
	}
	if strings.Contains(log, "private-password") {
		t.Fatal("run log leaked a credential")
	}
}

func TestRunLogUnknownTest(t *testing.T) {
	m, _ := setup(t)
	if _, err := m.Events("missing"); err == nil {
		t.Fatal("expected not found")
	}
}

func TestStageAt(t *testing.T) {
	stages := []scenario.Stage{{Duration: scenario.Duration{Duration: time.Minute}, TargetClients: 10}, {Duration: scenario.Duration{Duration: time.Minute}, TargetClients: 10}}
	for elapsed, want := range map[time.Duration]int{0: 0, 59 * time.Second: 0, time.Minute: 1, 3 * time.Minute: -1} {
		if got := stageAt(stages, elapsed); got != want {
			t.Fatalf("stageAt(%s) = %d, want %d", elapsed, got, want)
		}
	}
	if got := stageMessage(stages, 1); got != "Stage 2/2: hold 10 clients for 1m0s" {
		t.Fatal(got)
	}
}

func TestTelemetryChangeLogsTransitionsOnce(t *testing.T) {
	ok := emqx.FleetSample{Complete: true, Targets: []emqx.TargetSample{{Target: "a"}, {Target: "b"}}}
	bad := emqx.FleetSample{Targets: []emqx.TargetSample{{Target: "a"}, {Target: "b", Error: "timeout"}}}
	if level, msg := telemetryChange(nil, ok); msg != "" {
		t.Fatalf("healthy start logged %s %q", level, msg)
	}
	if level, msg := telemetryChange(&ok, bad); level != "warn" || msg != "Broker telemetry incomplete: b: timeout" {
		t.Fatalf("degraded: %s %q", level, msg)
	}
	if _, msg := telemetryChange(&bad, bad); msg != "" {
		t.Fatalf("repeated problem logged again: %q", msg)
	}
	if level, msg := telemetryChange(&bad, ok); level != "info" || !strings.Contains(msg, "complete again (2 targets") {
		t.Fatalf("recovered: %s %q", level, msg)
	}
}
