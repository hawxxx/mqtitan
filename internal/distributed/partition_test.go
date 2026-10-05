package distributed

import (
	"github.com/mqtitan/mqtitan/internal/scenario"
	"testing"
	"time"
)

func TestPartitionsPreservePopulationAndStages(t *testing.T) {
	s := scenario.Scenario{Clients: scenario.Clients{Count: 11, IDTemplate: "c-${sequence}"}, Stages: []scenario.Stage{{Duration: scenario.Duration{Duration: time.Second}, TargetClients: 5}, {Duration: scenario.Duration{Duration: time.Second}, TargetClients: 11}}, Workloads: []scenario.Workload{{Name: "sub", Type: "subscriber", Clients: 3}, {Name: "pub", Type: "publisher", Clients: 8}}}
	wantCount := []int{4, 4, 3}
	wantFirst := []int{4, 1, 0}
	total := 0
	for i := 0; i < 3; i++ {
		p, err := Partition(s, i, 3)
		if err != nil {
			t.Fatal(err)
		}
		if p.Clients.Count != wantCount[i] || p.Stages[0].TargetClients != wantFirst[i] {
			t.Fatalf("partition %d: %+v", i, p)
		}
		for _, w := range p.Workloads {
			total += w.Clients
		}
	}
	if total != 11 {
		t.Fatalf("population duplicated/lost: %d", total)
	}
}
