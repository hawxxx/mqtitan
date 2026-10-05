package platform

import (
	"fmt"
	"math"
)

type PlanInputs struct {
	Clients, FileDescriptors, MemoryBytes int64
	WireBytesPerSecond                    float64
	SourceIPs, DestinationTuples          int
}
type Capacity struct{ OpenFiles, AvailableMemoryBytes, EphemeralPorts, NICBitsPerSecond uint64 }

// AssessPlan compares estimates with measured ceilings, not certified capacity.
// Port accounting assumes connections share destination tuples; callers should
// supply the actual source address and broker destination population per worker.
func AssessPlan(p PlanInputs, c Capacity) []Check {
	if p.Clients < 0 || p.FileDescriptors < 0 || p.MemoryBytes < 0 || p.SourceIPs < 0 || p.DestinationTuples < 0 || p.WireBytesPerSecond < 0 || math.IsNaN(p.WireBytesPerSecond) || math.IsInf(p.WireBytesPerSecond, 0) {
		return []Check{{"plan", "invalid estimates", "WARN", "Review planner inputs"}}
	}
	ips, tuples := max(p.SourceIPs, 1), max(p.DestinationTuples, 1)
	portBudget := float64(c.EphemeralPorts) * float64(ips) * float64(tuples)
	return []Check{
		budgetCheck("plan files", float64(p.FileDescriptors), float64(c.OpenFiles), "descriptors", "Review process/container nofile limits before starting"),
		budgetCheck("plan ports", float64(p.Clients), portBudget, "source-port tuples", "Review source IPs, destination tuples, NAT capacity and connection churn"),
		budgetCheck("plan memory", float64(p.MemoryBytes), float64(c.AvailableMemoryBytes), "bytes", "Review available memory, container limits and measured memory per client"),
		budgetCheck("plan bandwidth", p.WireBytesPerSecond*8, float64(c.NICBitsPerSecond), "bits/s", "Review effective NIC/link capacity and measured bidirectional wire traffic"),
	}
}
func budgetCheck(name string, estimate, capacity float64, unit, recommendation string) Check {
	if capacity <= 0 {
		return Check{name, "capacity unknown", "INFO", "Measure this worker resource before increasing load"}
	}
	status, rec := "PASS", ""
	if estimate >= capacity {
		status, rec = "WARN", recommendation
	}
	return Check{name, fmt.Sprintf("estimate %.0f / measured ceiling %.0f %s", estimate, capacity, unit), status, rec}
}
