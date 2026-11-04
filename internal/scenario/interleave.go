package scenario

import "fmt"

// Workload orders for clients.workloadOrder.
const (
	// OrderContiguous gives each workload a contiguous block of the client
	// population in listed order. It is the default.
	OrderContiguous = "contiguous"
	// OrderInterleaved spreads every workload evenly over the client
	// population, so any contiguous slice of it (a worker partition, or the
	// prefix a stage target selects) holds a proportional mix of workloads.
	OrderInterleaved = "interleaved"
)

// Interleaved reports whether workloads are spread across the population.
func (s Scenario) Interleaved() bool { return s.Clients.WorkloadOrder == OrderInterleaved }

// PopulationSize is the global client count the workload layout spans. A worker
// partition covers a slice of it.
func (s Scenario) PopulationSize() int {
	if s.Clients.Population > 0 {
		return s.Clients.Population
	}
	return s.Clients.Count
}

func validWorkloadOrder(order string) error {
	switch order {
	case "", OrderContiguous, OrderInterleaved:
		return nil
	}
	return fmt.Errorf("clients.workloadOrder must be %q or %q", OrderContiguous, OrderInterleaved)
}

// WorkloadTable maps each of this scenario's clients (local sequence 1..Count,
// at index seq-1) to an index into s.Workloads. The index len(s.Workloads)
// stands for the default connection-only workload that covers clients no
// workload claims. It is meaningful only for interleaved scenarios.
//
// The layout is a deterministic function of the full population and the
// workload sizes, so a partition computes exactly the slice of the layout the
// unpartitioned scenario would give those clients. Each step picks the
// workload furthest behind its proportional share (ties go to the earlier
// workload), which keeps every workload within one client of its ideal count in
// any prefix.
func (s Scenario) WorkloadTable() []uint16 {
	n := s.PopulationSize()
	k := len(s.Workloads)
	want := make([]int, k+1)
	claimed := 0
	for i, w := range s.Workloads {
		want[i] = w.Clients
		claimed += w.Clients
	}
	want[k] = max(0, n-claimed)
	have := make([]int, k+1)
	start := s.Clients.PartitionStart
	out := make([]uint16, 0, s.Clients.Count)
	end := min(n, start+s.Clients.Count)
	for g := 0; g < end; g++ {
		best, bestDeficit := -1, int64(0)
		for i := 0; i <= k; i++ {
			if have[i] >= want[i] {
				continue
			}
			// Deficit against the ideal share after g+1 placements, scaled by n.
			d := int64(want[i])*int64(g+1) - int64(have[i])*int64(n)
			if best < 0 || d > bestDeficit {
				best, bestDeficit = i, d
			}
		}
		if best < 0 {
			best = k
		} else {
			have[best]++
		}
		if g >= start {
			out = append(out, uint16(best))
		}
	}
	return out
}
