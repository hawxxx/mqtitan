package scenario

import "testing"

func BenchmarkExpand(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Expand("devices/${sequence:08}/${clientId}/telemetry", i, "client-00000042")
	}
}
