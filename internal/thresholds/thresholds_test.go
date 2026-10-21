package thresholds

import (
	"github.com/mqtitan/mqtitan/internal/metrics"
	"testing"
	"time"
)

func TestStrictPercentThresholdAndLatency(t *testing.T) {
	s := metrics.Snapshot{ConnectAttempts: 10000, ConnectErrors: 1, PublishAttempts: 1000, Published: 1000, P95: 31 * time.Millisecond, P99: 141 * time.Millisecond, PublishLatencyCount: 1000, Histogram: []metrics.Bucket{{UpperBoundNs: time.Second, Count: 1000}}}
	results, err := Evaluate(map[string]string{"connection_error_rate": "<0.01%", "publish_latency_p95": "<50ms", "publish_latency_p99": "<100ms"}, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[0].Passed || !results[1].Passed || results[2].Passed {
		t.Fatalf("wrong verdicts: %+v", results)
	}
}

func TestRejectUnknownMetricOrExpression(t *testing.T) {
	for _, input := range []map[string]string{{"imaginary": "<1"}, {"publish_latency_p99": "<banana"}, {"message_error_rate": "<NaN%"}} {
		if err := Validate(input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
}

func TestNoObservationsCannotPass(t *testing.T) {
	r, err := Evaluate(map[string]string{"connection_error_rate": "<1%", "publish_latency_p99": "<1s"}, metrics.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r {
		if v.Passed {
			t.Fatalf("empty test passed: %+v", v)
		}
	}
}

func TestConnectEndToEndAndSubscribeThresholds(t *testing.T) {
	input := map[string]string{"connect_latency_p95": "<100ms", "end_to_end_latency_p99": "<20ms", "subscribe_error_rate": "<=1%"}
	s := metrics.Snapshot{PeakConnected: 5, ConnectP95: 40 * time.Millisecond, CorrelationSamples: 10, EndToEndP99: 25 * time.Millisecond, Subscriptions: 99, SubscribeErrors: 1}
	results, err := Evaluate(input, s)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted: connect_latency_p95, end_to_end_latency_p99, subscribe_error_rate.
	if len(results) != 3 || !results[0].Passed || results[1].Passed || !results[2].Passed {
		t.Fatalf("wrong verdicts: %+v", results)
	}
	empty, err := Evaluate(input, metrics.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range empty {
		if r.Passed || r.Observed != "no observations" {
			t.Fatalf("missing measurement passed: %+v", r)
		}
	}
	if err := Validate(map[string]string{"connect_latency_p95": "<5%"}); err == nil {
		t.Fatal("latency threshold accepted a percentage")
	}
}

func TestLatencyThresholdNeedsRecordedSamples(t *testing.T) {
	// Published messages without a merged histogram (missing or invalid worker
	// distributions) report zero percentiles; that must not pass "<1ms".
	r, err := Evaluate(map[string]string{"publish_latency_p99": "<1ms"}, metrics.Snapshot{Published: 10, PublishLatencyCount: 10})
	if err != nil || r[0].Passed || r[0].Observed != "no observations" {
		t.Fatalf("threshold passed without latency samples: %+v %v", r, err)
	}
}
