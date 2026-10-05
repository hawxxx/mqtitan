package scenario

import (
	"testing"
	"time"
)

func TestExpand(t *testing.T) {
	if got := Expand("gateway-${sequence:06}-${clientId}", 42, "c42"); got != "gateway-000042-c42" {
		t.Fatalf("got %q", got)
	}
}

func TestPlan(t *testing.T) {
	s := Scenario{Clients: Clients{Count: 100}, Stages: []Stage{{Duration: Duration{time.Minute}, TargetClients: 80}}, Workloads: []Workload{{Clients: 100, RatePerClient: 2, Payload: Payload{Size: 512}}}}
	p := s.Plan()
	if p.PeakClients != 80 || p.MessagesPerSecond != 160 {
		t.Fatalf("bad plan: %+v", p)
	}
}
