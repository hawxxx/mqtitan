package scenario

import "testing"

func interleavedScenario(count int, sizes ...int) Scenario {
	s := Scenario{Clients: Clients{Count: count, IDTemplate: "c-${sequence}", WorkloadOrder: OrderInterleaved}}
	for i, n := range sizes {
		s.Workloads = append(s.Workloads, Workload{Name: string(rune('a' + i)), Type: "subscriber", Topic: "t", Clients: n})
	}
	return s
}

func count(table []uint16, k int) []int {
	out := make([]int, k)
	for _, w := range table {
		out[w]++
	}
	return out
}

func TestWorkloadTableKeepsExactCountsAndSpreads(t *testing.T) {
	s := interleavedScenario(100, 30, 60) // 10 left over become connection-only
	table := s.WorkloadTable()
	if len(table) != 100 {
		t.Fatalf("len = %d", len(table))
	}
	if got := count(table, 3); got[0] != 30 || got[1] != 60 || got[2] != 10 {
		t.Fatalf("counts = %v", got)
	}
	// Every prefix stays within one client of its proportional share.
	have := [3]int{}
	for g, w := range table {
		have[w]++
		for i, want := range []int{30, 60, 10} {
			ideal := float64(want) * float64(g+1) / 100
			if d := float64(have[i]) - ideal; d > 1 || d < -1 {
				t.Fatalf("prefix %d workload %d has %d, ideal %.1f", g+1, i, have[i], ideal)
			}
		}
	}
}

func TestPartitionedTablesConcatenateToTheWholeLayout(t *testing.T) {
	whole := interleavedScenario(103, 40, 63)
	want := whole.WorkloadTable()
	var got []uint16
	start := 0
	for _, size := range []int{26, 26, 26, 25} {
		p := whole
		p.Clients.Count, p.Clients.Population, p.Clients.PartitionStart = size, 103, start
		got = append(got, p.WorkloadTable()...)
		start += size
	}
	if len(got) != len(want) {
		t.Fatalf("len %d vs %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("client %d: partition says %d, whole says %d", i+1, got[i], want[i])
		}
	}
}

func TestWorkloadOrderValidation(t *testing.T) {
	s := interleavedScenario(10, 5, 5)
	s.Clients.WorkloadOrder = "random"
	if err := validWorkloadOrder(s.Clients.WorkloadOrder); err == nil {
		t.Fatal("unknown order accepted")
	}
}

func TestInterleavedPlanDoesNotWarnWhenEveryWorkloadIsReached(t *testing.T) {
	s := interleavedScenario(100, 50, 50)
	s.Stages = []Stage{{Duration: Duration{Duration: 1e9}, TargetClients: 10}}
	if w := s.Plan().Warnings; len(w) != 0 {
		t.Fatalf("warnings = %v", w)
	}
	s.Clients.WorkloadOrder = ""
	if w := s.Plan().Warnings; len(w) == 0 {
		t.Fatal("contiguous layout should warn that the second workload never connects")
	}
}
