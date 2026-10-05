package api

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"time"
)

func (m *Manager) prometheus(w http.ResponseWriter) {
	tests, err := m.List()
	if err != nil {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	counters := map[string]uint64{"connections_total": 0, "connection_errors_total": 0, "messages_published_total": 0, "messages_received_total": 0, "publish_errors_total": 0, "bytes_sent_total": 0, "bytes_received_total": 0, "reconnects_total": 0, "disconnects_total": 0, "publishes_cancelled_total": 0}
	var connected int64
	var count uint64
	sum := 0.0
	buckets := map[time.Duration]uint64{}
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
		counters["disconnects_total"] += s.Disconnects
		counters["publishes_cancelled_total"] += s.PublishCancelled
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
	fmt.Fprintf(w, "emqxload_publish_latency_seconds_bucket{le=\"+Inf\"} %d\nemqxload_publish_latency_seconds_sum %g\nemqxload_publish_latency_seconds_count %d\n", count, sum, count)
	// Quantiles describe the latest run. Historical comparisons use the API.
	if len(tests) > 0 {
		s := tests[0].Snapshot
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
