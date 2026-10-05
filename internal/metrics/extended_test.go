package metrics

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestJSONSnapshotContract(t *testing.T) {
	b, _ := json.Marshal(New().Snapshot())
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"connected", "targetClients", "connecting", "received", "bytesReceived", "p999", "peakConnected"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
}

func TestShardedConcurrentMetricsAndErrors(t *testing.T) {
	m := New()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.RecordPublishLatencyFor(shard, time.Millisecond)
				m.RecordConnectionLatencyFor(shard, 2*time.Millisecond)
				m.RecordError("connect")
				m.RecordError("arbitrary-secret")
			}
		}(i)
	}
	wg.Wait()
	s := m.Snapshot()
	if s.P95 > 2*time.Millisecond || s.ConnectP95 < 2*time.Millisecond || s.Errors["connect"] != 6400 || s.Errors["other"] != 6400 {
		t.Fatalf("bad sharded counts %+v", s)
	}
	if len(s.Errors) != 2 {
		t.Fatal("unbounded error keys")
	}
	d := m.ExportDistribution()
	var count int64
	for _, n := range d.Publish.Counts {
		count += n
	}
	if count != 6400 {
		t.Fatalf("lost histogram samples: %d", count)
	}
}

func TestMergeUsesHistogramSamples(t *testing.T) {
	a, b := New(), New()
	for i := 0; i < 99; i++ {
		a.RecordPublishLatency(time.Millisecond)
	}
	b.RecordPublishLatency(time.Second)
	a.Published.Add(99)
	b.Published.Add(1)
	got := MergeSnapshots([]Snapshot{a.Snapshot(), b.Snapshot()}, []Distribution{a.ExportDistribution(), b.ExportDistribution()})
	if got.Published != 100 || got.P50 > 2*time.Millisecond || got.P99 > 2*time.Millisecond || got.P999 < 999*time.Millisecond {
		t.Fatalf("bad aggregate %+v", got)
	}
}

func TestRejectMalformedDistribution(t *testing.T) {
	d := New().ExportDistribution()
	d.Publish.Counts = d.Publish.Counts[:1]
	if err := ValidateDistribution(d); err == nil {
		t.Fatal("malformed HDR bucket array accepted")
	}
	_ = MergeSnapshots(nil, []Distribution{d})
}

func TestHistogramAndExactLatencySummary(t *testing.T) {
	m := New()
	m.RecordPublishLatency(123 * time.Nanosecond)
	m.RecordPublishLatency(2 * time.Microsecond)
	s := m.Snapshot()
	if s.PublishLatencyCount != 2 || s.PublishLatencySum != 2123*time.Nanosecond || s.Min != 123*time.Nanosecond || s.Max != 2*time.Microsecond || s.Avg != 1061*time.Nanosecond {
		t.Fatalf("wrong summary %+v", s)
	}
	if len(s.Histogram) != 20 || s.Histogram[0].Count != 1 || s.Histogram[1].Count != 1 {
		t.Fatalf("wrong histogram %+v", s.Histogram)
	}
	var count uint64
	for _, b := range s.Histogram {
		count += b.Count
	}
	if count != 2 {
		t.Fatal("histogram lost samples")
	}
	merged := MergeSnapshots([]Snapshot{s, s}, []Distribution{m.ExportDistribution(), m.ExportDistribution()})
	if merged.PublishLatencySum != 4246*time.Nanosecond || merged.PublishLatencyCount != 4 || merged.Avg != 1061*time.Nanosecond {
		t.Fatalf("wrong merged summary %+v", merged)
	}
}
