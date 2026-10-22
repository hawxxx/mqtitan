package thresholds

import (
	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"testing"
	"time"
)

func TestStrictPercentThresholdAndLatency(t *testing.T) {
	s := metrics.Snapshot{ConnectAttempts: 10000, ConnectErrors: 1, PublishAttempts: 1000, Published: 1000, P95: 31 * time.Millisecond, P99: 141 * time.Millisecond, PublishLatencyCount: 1000, Histogram: []metrics.Bucket{{UpperBoundNs: time.Second, Count: 1000}}}
	results, err := Evaluate(map[string]string{"connection_error_rate": "<0.01%", "publish_latency_p95": "<50ms", "publish_latency_p99": "<100ms"}, s, nil)
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
	r, err := Evaluate(map[string]string{"connection_error_rate": "<1%", "publish_latency_p99": "<1s"}, metrics.Snapshot{}, nil)
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
	results, err := Evaluate(input, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted: connect_latency_p95, end_to_end_latency_p99, subscribe_error_rate.
	if len(results) != 3 || !results[0].Passed || results[1].Passed || !results[2].Passed {
		t.Fatalf("wrong verdicts: %+v", results)
	}
	empty, err := Evaluate(input, metrics.Snapshot{}, nil)
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
	r, err := Evaluate(map[string]string{"publish_latency_p99": "<1ms"}, metrics.Snapshot{Published: 10, PublishLatencyCount: 10}, nil)
	if err != nil || r[0].Passed || r[0].Observed != "no observations" {
		t.Fatalf("threshold passed without latency samples: %+v %v", r, err)
	}
}

func balance(cv, skew float64, targets, zones int, complete bool) *emqx.Balance {
	b := &emqx.Balance{Complete: complete, TargetCV: cv, ZoneSkew: skew}
	for i := 0; i < targets; i++ {
		b.Targets = append(b.Targets, emqx.Group{Key: string(rune('a' + i)), Connections: 1})
	}
	for i := 0; i < zones; i++ {
		b.Zones = append(b.Zones, emqx.Group{Key: string(rune('x' + i)), Connections: 1})
	}
	return b
}

func TestBalanceThresholds(t *testing.T) {
	in := map[string]string{"broker_connection_cv": "<10%", "zone_connection_skew": "<=20%"}
	r, err := Evaluate(in, metrics.Snapshot{}, balance(0.05, 0.1, 2, 2, true))
	if err != nil || !Passed(r) || r[0].Observed != "5.000000%" {
		t.Fatalf("%v %+v", err, r)
	}
	r, _ = Evaluate(in, metrics.Snapshot{}, balance(0.2, 0.1, 2, 2, true))
	if Passed(r) || r[0].Passed || !r[1].Passed {
		t.Fatalf("%+v", r)
	}
}

func TestBalanceThresholdNoData(t *testing.T) {
	in := map[string]string{"broker_connection_cv": "<10%", "zone_connection_skew": "<=20%"}
	r, _ := Evaluate(in, metrics.Snapshot{}, nil)
	for _, x := range r {
		if x.Passed || x.Observed != "no observations" {
			t.Fatalf("%+v", x)
		}
	}
}

func TestBalanceThresholdFailsOnIncompleteSample(t *testing.T) {
	in := map[string]string{"broker_connection_cv": "<10%", "zone_connection_skew": "<=20%"}
	r, _ := Evaluate(in, metrics.Snapshot{}, balance(0, 0, 2, 2, false))
	for _, x := range r {
		if x.Passed || x.Observed != "incomplete broker telemetry" {
			t.Fatalf("%+v", x)
		}
	}
}

func TestBalanceThresholdNeedsTwoGroups(t *testing.T) {
	in := map[string]string{"broker_connection_cv": "<10%", "zone_connection_skew": "<=20%"}
	r, _ := Evaluate(in, metrics.Snapshot{}, balance(0, 0, 1, 1, true))
	for _, x := range r {
		if x.Passed || x.Observed != "no observations" {
			t.Fatalf("%+v", x)
		}
	}
	r, _ = Evaluate(in, metrics.Snapshot{}, balance(0, 0, 3, 0, true))
	if !r[0].Passed || r[1].Passed || r[1].Observed != "no observations" {
		t.Fatalf("%+v", r)
	}
}

func TestBalancePercentMayExceed100(t *testing.T) {
	if err := Validate(map[string]string{"zone_connection_skew": "<150%", "broker_connection_cv": "<=300%"}); err != nil {
		t.Fatal(err)
	}
	if err := Validate(map[string]string{"connection_error_rate": "<150%"}); err == nil {
		t.Fatal("error rate must stay capped")
	}
	if err := Validate(map[string]string{"zone_connection_skew": "<5ms"}); err == nil {
		t.Fatal("balance threshold must be a percentage")
	}
}

func TestBalanceThresholdAllZeroFleetHasNoObservations(t *testing.T) {
	in := map[string]string{"broker_connection_cv": "<10%", "zone_connection_skew": "<=20%"}
	b := &emqx.Balance{Complete: true,
		Targets: []emqx.Group{{Key: "a"}, {Key: "b"}}, Zones: []emqx.Group{{Key: "x"}, {Key: "y"}}, TargetRatio: 1, ZoneRatio: 1}
	r, _ := Evaluate(in, metrics.Snapshot{}, b)
	for _, x := range r {
		if x.Passed || x.Observed != "no observations" {
			t.Fatalf("%+v", x)
		}
	}
}

func TestBalanceThresholdFailsWhenTelemetryFailsAtPeak(t *testing.T) {
	at := func(m int) time.Time { return time.Unix(int64(m), 0) }
	sample := func(m int, complete bool, a, b float64) emqx.FleetSample {
		return emqx.FleetSample{Timestamp: at(m), Complete: complete, Targets: []emqx.TargetSample{{Target: "a", Connections: a}, {Target: "b", Connections: b}}}
	}
	in := map[string]string{"broker_connection_cv": "<10%"}
	bad := sample(2, false, 300, 0)
	bad.Targets[1] = emqx.TargetSample{Target: "b", Error: "boom"}
	b, ok := emqx.PeakBalance([]emqx.FleetSample{sample(1, true, 200, 200), bad})
	if !ok || b.Complete {
		t.Fatalf("%v %+v", ok, b)
	}
	r, _ := Evaluate(in, metrics.Snapshot{}, &b)
	if r[0].Passed || r[0].Observed != "incomplete broker telemetry" {
		t.Fatalf("%+v", r)
	}
	early := sample(1, false, 10, 10)
	b, _ = emqx.PeakBalance([]emqx.FleetSample{early, sample(2, true, 200, 200)})
	if !b.Complete {
		t.Fatalf("%+v", b)
	}
}
