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

// BenchmarkSnapshot measures the once-per-second merge of every shard's
// histograms, which runs off the publish path for each run and worker.
func BenchmarkSnapshot(b *testing.B) {
	m := New()
	for i := range 100_000 {
		d := time.Duration(i%5000+1) * 10 * time.Microsecond
		m.RecordPublishLatencyFor(i, d)
		m.RecordEndToEndLatencyFor(i, d)
		m.RecordConnectionLatencyFor(i, d)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.Snapshot()
	}
}

func BenchmarkExportDistribution(b *testing.B) {
	m := New()
	for i := range 100_000 {
		m.RecordPublishLatencyFor(i, time.Duration(i%5000+1)*10*time.Microsecond)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.ExportDistribution()
	}
}

func TestCorrectedLatencyCountsQueuedSlots(t *testing.T) {
	// 100 msg/s: 99 publishes take 1ms, one stalls for 1s. Uncorrected, the
	// stall is a single sample and p99 stays at 1ms; the ~99 slots queued
	// behind it must appear in the distribution.
	m := New()
	for range 99 {
		m.RecordPublishLatencyCorrected(1, time.Millisecond, 10*time.Millisecond)
	}
	m.RecordPublishLatencyCorrected(1, time.Second, 10*time.Millisecond)
	s := m.Snapshot()
	if s.P99 < 900*time.Millisecond {
		t.Fatalf("p99 = %s, stall hidden by coordinated omission", s.P99)
	}
	if s.PublishLatencyCount != 100 || s.Max != time.Second {
		t.Fatalf("summary counts synthetic samples: count=%d max=%s", s.PublishLatencyCount, s.Max)
	}
}

func TestHistogramBucketsUseUpperEdge(t *testing.T) {
	m := New()
	m.RecordPublishLatency(5*time.Millisecond + time.Microsecond)
	for _, b := range m.Snapshot().Histogram {
		if b.Count > 0 && b.UpperBoundNs != 10*time.Millisecond {
			t.Fatalf("5.001ms sample landed in le=%s", b.UpperBoundNs)
		}
	}
}

func TestTimeoutIsItsOwnErrorCategory(t *testing.T) {
	m := New()
	m.RecordError("timeout")
	m.RecordError("bogus")
	if e := m.Snapshot().Errors; e["timeout"] != 1 || e["other"] != 1 {
		t.Fatalf("errors = %v", e)
	}
}

func TestBackfillMatchesExactCorrectionAndIsBounded(t *testing.T) {
	exact, bounded := histogram(), histogram()
	_ = exact.RecordCorrectedValue(1_000_000, 10_000) // 1s stall, 10ms period
	_ = bounded.RecordValue(1_000_000)
	backfill(bounded, 1_000_000, 10_000)
	if exact.TotalCount() != bounded.TotalCount() {
		t.Fatalf("count = %d, want %d", bounded.TotalCount(), exact.TotalCount())
	}
	for _, q := range []float64{50, 90, 99} {
		e, b := exact.ValueAtQuantile(q), bounded.ValueAtQuantile(q)
		if diff := float64(b-e) / float64(e); diff > 0.01 || diff < -0.01 {
			t.Fatalf("p%v = %d, exact %d", q, b, e)
		}
	}
}

func BenchmarkCorrectedWorstCase(b *testing.B) {
	m := New()
	b.ReportAllocs()
	for b.Loop() {
		// 10s stall at a 1µs period: ten million missing slots.
		m.RecordPublishLatencyCorrected(1, 10*time.Second, time.Microsecond)
	}
}

func TestConnectFailuresMergeAndSumToErrors(t *testing.T) {
	a, b := New(), New()
	for i, stage := range []string{"tcp", "tcp", "dns", "mqtt_connack", "bogus"} {
		m := a
		if i%2 == 1 {
			m = b
		}
		m.ConnectErrors.Add(1)
		code := -1
		if stage == "mqtt_connack" {
			code = 135
		}
		m.RecordConnectFailure(stage, code)
	}
	sa, sb := a.Snapshot(), b.Snapshot()
	if sa.ConnectFailures["tcp"] != 1 || len(sa.ConnackReasons) != 0 {
		t.Fatalf("%+v %+v", sa.ConnectFailures, sa.ConnackReasons)
	}
	m := MergeSnapshots([]Snapshot{sa, sb}, nil)
	want := map[string]uint64{"tcp": 2, "dns": 1, "mqtt_connack": 1, "other": 1}
	var sum uint64
	for k, n := range m.ConnectFailures {
		sum += n
		if want[k] != n {
			t.Fatalf("stage %s = %d, want %d (%v)", k, n, want[k], m.ConnectFailures)
		}
	}
	if sum != m.ConnectErrors || len(m.ConnectFailures) != len(want) {
		t.Fatalf("sum %d, errors %d, %v", sum, m.ConnectErrors, m.ConnectFailures)
	}
	if m.ConnackReasons["135"] != 1 || len(m.ConnackReasons) != 1 {
		t.Fatalf("%v", m.ConnackReasons)
	}
	if got := MergeSnapshots([]Snapshot{New().Snapshot()}, nil); got.ConnectFailures != nil || got.ConnackReasons != nil {
		t.Fatalf("empty merge leaked maps: %+v", got)
	}
}

func TestRecordConnectFailureNoAlloc(t *testing.T) {
	m := New()
	if n := testing.AllocsPerRun(1000, func() { m.RecordConnectFailure("mqtt_connack", 135) }); n != 0 {
		t.Fatalf("allocs=%v", n)
	}
}
