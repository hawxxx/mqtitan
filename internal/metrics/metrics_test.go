package metrics

import (
	"testing"
	"time"
)

func TestSnapshotPercentiles(t *testing.T) {
	m := New()
	for i := 1; i <= 100; i++ {
		m.RecordPublishLatency(time.Duration(i) * time.Millisecond)
	}
	s := m.Snapshot()
	if s.P50 < 49*time.Millisecond || s.P50 > 51*time.Millisecond {
		t.Fatalf("p50=%s", s.P50)
	}
	if s.P99 < 98*time.Millisecond || s.P99 > 100*time.Millisecond {
		t.Fatalf("p99=%s", s.P99)
	}
}

func BenchmarkRecordPublishLatency(b *testing.B) {
	m := New()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.RecordPublishLatency(time.Millisecond)
		}
	})
}
