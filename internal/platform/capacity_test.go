package platform

import (
	"strings"
	"testing"
)

func TestAssessPlanWarnsMeasuredResourceCeilings(t *testing.T) {
	checks := AssessPlan(PlanInputs{Clients: 50000, FileDescriptors: 100000, MemoryBytes: 2 << 30, WireBytesPerSecond: 200e6}, Capacity{OpenFiles: 65536, AvailableMemoryBytes: 1 << 30, EphemeralPorts: 28000, NICBitsPerSecond: 1e9})
	if len(checks) != 4 {
		t.Fatalf("want four budget checks: %v", checks)
	}
	for _, c := range checks {
		if c.Status != "WARN" {
			t.Fatalf("missing ceiling warning: %+v", c)
		}
	}
}

func TestAssessPlanUsesSourceIPsAndReportsUnknowns(t *testing.T) {
	checks := AssessPlan(PlanInputs{Clients: 50000, SourceIPs: 2, DestinationTuples: 1}, Capacity{EphemeralPorts: 28000})
	if len(checks) != 4 {
		t.Fatalf("missing resource observations: %v", checks)
	}
	for _, c := range checks {
		if c.Name == "plan ports" && c.Status != "PASS" {
			t.Fatalf("did not account source addresses: %+v", c)
		}
		if c.Name != "plan ports" && !strings.Contains(c.Value, "unknown") {
			t.Fatalf("invented capacity: %+v", c)
		}
	}
}
