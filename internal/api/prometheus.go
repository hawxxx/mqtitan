package api

import (
	"fmt"
	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) prometheus(w http.ResponseWriter) {
	tests, err := m.List()
	if err != nil {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	counters := map[string]uint64{"connections_total": 0, "connection_errors_total": 0, "messages_published_total": 0, "messages_received_total": 0, "publish_errors_total": 0, "bytes_sent_total": 0, "bytes_received_total": 0, "reconnects_total": 0, "churned_total": 0, "churn_reconnects_total": 0, "disconnects_total": 0, "publishes_cancelled_total": 0, "publish_slots_missed_total": 0, "correlation_rejected_total": 0}
	var connected int64
	var count uint64
	sum := 0.0
	buckets := map[time.Duration]uint64{}
	stages := map[string]uint64{}
	connack := map[int]uint64{}
	for _, t := range tests {
		s := t.Snapshot
		if t.Status == "running" {
			connected += s.Connected
		}
		counters["connections_total"] += s.ConnectAttempts
		counters["connection_errors_total"] += s.ConnectErrors
		counters["messages_published_total"] += s.Published
		counters["messages_received_total"] += s.Received
		counters["publish_errors_total"] += s.PublishErrors
		counters["bytes_sent_total"] += s.BytesSent
		counters["bytes_received_total"] += s.BytesReceived
		counters["reconnects_total"] += s.Reconnects
		counters["churned_total"] += s.Churned
		counters["churn_reconnects_total"] += s.ChurnReconnects
		counters["disconnects_total"] += s.Disconnects
		counters["publishes_cancelled_total"] += s.PublishCancelled
		counters["publish_slots_missed_total"] += s.PublishSlotsMissed
		counters["correlation_rejected_total"] += s.CorrelationRejected
		for stage, n := range s.ConnectFailures {
			stages[stage] += n // only known stages are emitted below
		}
		for code, n := range s.ConnackReasons {
			if v, ok := metrics.ParseConnackCode(code); ok {
				connack[v] += n
			}
		}
		count += s.PublishLatencyCount
		sum += s.PublishLatencySum.Seconds()
		for _, b := range s.Histogram {
			buckets[b.UpperBoundNs] += b.Count
		}
	}
	fmt.Fprintf(w, "# TYPE emqxload_clients_connected gauge\nemqxload_clients_connected %d\n", connected)
	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "# TYPE emqxload_%s counter\nemqxload_%s %d\n", name, name, counters[name])
	}
	fmt.Fprintln(w, "# TYPE emqxload_connect_failures_total counter")
	for _, stage := range metrics.ConnectStages() {
		fmt.Fprintf(w, "emqxload_connect_failures_total{stage=%q} %d\n", stage, stages[stage])
	}
	codes := make([]int, 0, len(connack))
	for code := range connack {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	if len(codes) > 0 {
		fmt.Fprintln(w, "# TYPE emqxload_connack_refusals_total counter")
	}
	for _, code := range codes {
		fmt.Fprintf(w, "emqxload_connack_refusals_total{code=\"%d\"} %d\n", code, connack[code])
	}
	fmt.Fprintln(w, "# TYPE emqxload_publish_latency_seconds histogram")
	bounds := make([]time.Duration, 0, len(buckets))
	for b := range buckets {
		bounds = append(bounds, b)
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	var cumulative uint64
	for _, b := range bounds {
		cumulative += buckets[b]
		fmt.Fprintf(w, "emqxload_publish_latency_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(b.Seconds(), 'g', -1, 64), cumulative)
	}
	// Buckets include coordinated-omission corrections for slots that queued
	// behind slow publishes; keep +Inf and _count consistent with them.
	count = max(count, cumulative)
	fmt.Fprintf(w, "emqxload_publish_latency_seconds_bucket{le=\"+Inf\"} %d\nemqxload_publish_latency_seconds_sum %g\nemqxload_publish_latency_seconds_count %d\n", count, sum, count)
	// Quantiles describe the latest run. Historical comparisons use the API.
	if len(tests) > 0 {
		latest := tests[0]
		for _, t := range tests[1:] {
			if t.StartedAt.After(latest.StartedAt) {
				latest = t
			}
		}
		s := latest.Snapshot
		writeLabelSeries(w, latest.Snapshot)
		m.writeBalanceSeries(w, latest)
		fmt.Fprintln(w, "# TYPE emqxload_e2e_latency_seconds gauge")
		for _, p := range []struct {
			q string
			v time.Duration
		}{{"0.5", s.EndToEndP50}, {"0.95", s.EndToEndP95}, {"0.99", s.EndToEndP99}} {
			fmt.Fprintf(w, "emqxload_e2e_latency_seconds{quantile=%q} %g\n", p.q, p.v.Seconds())
		}
	}
	fmt.Fprintln(w, "# TYPE emqxload_worker_memory_bytes gauge")
	for _, worker := range m.workers.list() {
		fmt.Fprintf(w, "emqxload_worker_memory_bytes{worker=%q} %d\n", worker.ID, worker.MemoryBytes)
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	fmt.Fprintf(w, "emqxload_worker_memory_bytes{worker=\"local\"} %d\n", memory.Sys)
}

// writeLabelSeries exports the address and DNS distribution of one test.
// Rows come from stored snapshots, so each list is cut at the cardinality cap.
func writeLabelSeries(w io.Writer, s metrics.Snapshot) {
	limit := func(l []metrics.LabelCount) []metrics.LabelCount { return l[:min(len(l), metrics.MaxLabelKeys+1)] }
	type series struct {
		name, kind string
		rows       []metrics.LabelCount
		line       func(r metrics.LabelCount) string
		value      func(r metrics.LabelCount) string
	}
	for _, sr := range []series{
		{"emqxload_remote_connects_total", "counter", s.RemoteAddresses, func(r metrics.LabelCount) string {
			return `{address="` + promLabel(r.Key) + `",label="` + promLabel(r.Label) + `"}`
		}, func(r metrics.LabelCount) string { return strconv.FormatUint(r.Connects, 10) }},
		{"emqxload_remote_active", "gauge", s.RemoteAddresses, func(r metrics.LabelCount) string {
			return `{address="` + promLabel(r.Key) + `",label="` + promLabel(r.Label) + `"}`
		}, func(r metrics.LabelCount) string { return strconv.FormatInt(r.Active, 10) }},
		{"emqxload_address_label_active", "gauge", s.AddressLabels, func(r metrics.LabelCount) string { return `{label="` + promLabel(r.Key) + `"}` }, func(r metrics.LabelCount) string { return strconv.FormatInt(r.Active, 10) }},
		{"emqxload_dns_answers_total", "counter", s.DNSAnswers, func(r metrics.LabelCount) string { return `{address="` + promLabel(r.Key) + `"}` }, func(r metrics.LabelCount) string { return strconv.FormatUint(r.Connects, 10) }},
		{"emqxload_dns_errors_total", "counter", s.DNSErrors, func(r metrics.LabelCount) string { return `{resolver="` + promLabel(r.Key) + `"}` }, func(r metrics.LabelCount) string { return strconv.FormatUint(r.Connects, 10) }},
	} {
		if len(sr.rows) == 0 {
			continue
		}
		fmt.Fprintf(w, "# TYPE %s %s\n", sr.name, sr.kind)
		for _, r := range limit(sr.rows) {
			fmt.Fprintf(w, "%s%s %s\n", sr.name, sr.line(r), sr.value(r))
		}
	}
}

// writeBalanceSeries exports the latest broker sample and the peak balance of
// one test. Nothing is written for a test without telemetry.
func (m *Manager) writeBalanceSeries(w io.Writer, t Test) {
	samples, err := m.Brokers(t.ID)
	if err != nil {
		return
	}
	var latest *emqx.FleetSample
	for i := len(samples) - 1; i >= 0; i-- {
		if !samples[i].Baseline {
			latest = &samples[i]
			break
		}
	}
	if latest == nil {
		return
	}
	fmt.Fprintln(w, "# TYPE emqxload_broker_connections gauge")
	for _, s := range latest.Targets[:min(len(latest.Targets), 1024)] {
		if s.Error == "" {
			fmt.Fprintf(w, "emqxload_broker_connections{target=\"%s\",zone=\"%s\"} %g\n", promLabel(s.Target), promLabel(s.Zone), s.Connections)
		}
	}
	complete := 0
	if latest.Complete {
		complete = 1
	}
	balance := t.Balance
	if balance == nil { // still running: derive it from the live samples
		if peak, ok := emqx.PeakBalance(samples); ok {
			balance = &peak
		}
	}
	if balance != nil && balance.CVObserved() {
		fmt.Fprintf(w, "# TYPE emqxload_broker_connection_cv gauge\nemqxload_broker_connection_cv %g\n", balance.TargetCV)
	}
	if balance != nil && balance.SkewObserved() {
		fmt.Fprintf(w, "# TYPE emqxload_zone_connection_skew_ratio gauge\nemqxload_zone_connection_skew_ratio %g\n", balance.ZoneSkew)
	}
	fmt.Fprintf(w, "# TYPE emqxload_broker_telemetry_complete gauge\nemqxload_broker_telemetry_complete %d\n", complete)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// promLabel escapes a label value for the Prometheus text format, which only
// knows \\, \" and \n; other bytes, including tabs and UTF-8, are literal.
// Invalid UTF-8 from worker reports is replaced, since values must be UTF-8.
func promLabel(v string) string { return labelEscaper.Replace(strings.ToValidUTF8(v, "\uFFFD")) }
