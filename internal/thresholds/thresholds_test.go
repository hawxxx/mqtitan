package thresholds

import (
	"github.com/mqtitan/mqtitan/internal/metrics"
	"testing"
	"time"
)

func TestStrictPercentThresholdAndLatency(t *testing.T) {
	s := metrics.Snapshot{ConnectAttempts: 10000, ConnectErrors: 1, PublishAttempts: 1000, Published: 1000, P95: 31 * time.Millisecond, P99: 141 * time.Millisecond}
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
