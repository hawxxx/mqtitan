package metrics

import (
	"errors"
	hdr "github.com/HdrHistogram/hdrhistogram-go"
	"math"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const shardCount = 32
const maxLatency = int64(10 * time.Minute / time.Microsecond)

var histogramShape = histogram().Export()

func histogram() *hdr.Histogram { return hdr.New(1, maxLatency, 3) }

type latencyShard struct {
	mu                            sync.Mutex
	publish, endToEnd, connection *hdr.Histogram
	summary                       latencySummary
}
type latencySummary struct {
	count         uint64
	sum, min, max time.Duration
}

func (s *latencySummary) add(d time.Duration) {
	s.count++
	s.sum = saturateDuration(s.sum, d)
	if s.min == 0 || d < s.min {
		s.min = d
	}
	if d > s.max {
		s.max = d
	}
}
func saturateDuration(a, b time.Duration) time.Duration {
	if b > time.Duration(math.MaxInt64)-a {
		return time.Duration(math.MaxInt64)
	}
	return a + b
}

type Counters struct {
	TargetClients      atomic.Int64
	Connecting         atomic.Int64
	PeakConnected      atomic.Int64
	Received           atomic.Uint64
	BytesReceived      atomic.Uint64
	Subscriptions      atomic.Uint64
	SubscribeErrors    atomic.Uint64
	CorrelationSamples atomic.Uint64
	ConnectAttempts    atomic.Uint64
	Connected          atomic.Int64
	ConnectErrors      atomic.Uint64
	ConnectCancelled   atomic.Uint64
	PublishAttempts    atomic.Uint64
	Published          atomic.Uint64
	PublishErrors      atomic.Uint64
	PublishCancelled   atomic.Uint64
	BytesSent          atomic.Uint64
	Reconnects         atomic.Uint64
	Churned            atomic.Uint64
	ChurnReconnects    atomic.Uint64
	Disconnects        atomic.Uint64
	// PublishSlotsMissed counts scheduled publish slots skipped because the
	// previous publish of that client had not completed: the generator, or
	// the acknowledgement path, could not sustain the requested rate.
	PublishSlotsMissed atomic.Uint64
	// CorrelationRejected counts correlated messages whose timestamp was in
	// the future or more than ten minutes old, typically clock skew between
	// publisher and subscriber hosts.
	CorrelationRejected atomic.Uint64
	nextShard           atomic.Uint64
	shards              [shardCount]latencyShard
	errors              [len(errorCategories)]atomic.Uint64
	connectFailures     [len(connectStages)]atomic.Uint64
	connackReasons      [256]atomic.Uint64
	remotes             labelSet[netip.AddrPort]
	dnsAnswers          labelSet[netip.Addr]
	dnsErrors           labelSet[netip.AddrPort]
	labelCfg            atomic.Pointer[labelConfig]
}

// connectStages mirrors the stage constants in internal/mqtt; the last entry
// ("other") absorbs unknown stages so the stage counts always sum to ConnectErrors.
var connectStages = [...]string{"dns", "tcp", "tls_handshake", "tls_verify_chain", "tls_verify_hostname", "tls_alert", "websocket_upgrade", "mqtt_connack", "timeout", "other"}

// ConnectStages returns the bounded connect failure stages in stable order.
func ConnectStages() []string { return connectStages[:] }

// errorCategories are the bounded error classes; "timeout" is an operation
// that exceeded operationTimeout, which the client then closed itself.
var errorCategories = [...]string{"connect", "publish", "subscribe", "disconnect", "other", "timeout"}

func New() *Counters { return &Counters{} }
func (c *Counters) ClientConnected() {
	v := c.Connected.Add(1)
	for {
		p := c.PeakConnected.Load()
		if v <= p || c.PeakConnected.CompareAndSwap(p, v) {
			break
		}
	}
}
func (c *Counters) RecordError(category string) {
	i := 4
	for j, name := range errorCategories {
		if name == category {
			i = j
		}
	}
	c.errors[i].Add(1)
}

// RecordConnectFailure counts one classified connect failure; code is the
// CONNACK code for mqtt_connack and negative otherwise. It does not allocate.
func (c *Counters) RecordConnectFailure(stage string, code int) {
	i := len(connectStages) - 1
	for j, name := range connectStages {
		if name == stage {
			i = j
			break
		}
	}
	c.connectFailures[i].Add(1)
	if code >= 0 && code < len(c.connackReasons) {
		c.connackReasons[code].Add(1)
	}
}
func (c *Counters) record(shard int, kind byte, d time.Duration) {
	c.recordCorrected(shard, kind, d, 0)
}

// recordCorrected records d and, when expected > 0, the samples that slots
// queued behind a slow operation would have observed (HdrHistogram's
// coordinated-omission correction). The min/avg/max summary keeps only d.
func (c *Counters) recordCorrected(shard int, kind byte, d, expected time.Duration) {
	s := &c.shards[uint(shard)%shardCount]
	s.mu.Lock()
	defer s.mu.Unlock()
	h := &s.publish
	d = min(10*time.Minute, max(time.Nanosecond, d))
	if kind == 1 {
		h = &s.endToEnd
	} else if kind == 2 {
		h = &s.connection
	} else {
		s.summary.add(d)
	}
	if *h == nil {
		*h = histogram()
	}
	v := min(maxLatency, max(1, d.Microseconds()))
	_ = (*h).RecordValue(v)
	if interval := expected.Microseconds(); interval > 0 && v >= 2*interval {
		backfill(*h, v, interval)
	}
}

// backfill adds the samples that slots queued behind a v-long operation
// would have observed: v-interval, v-2*interval, ... down to interval, as
// HdrHistogram's RecordCorrectedValue does. That loop runs once per missing
// sample, which a long stall at a microsecond period turns into millions of
// iterations under the shard lock, so the ramp is recorded in at most 256
// evenly spaced steps with the same total count.
func backfill(h *hdr.Histogram, v, interval int64) {
	n := v/interval - 1
	steps := min(n, 256)
	for k := range steps {
		// Step k covers missing samples [k*n/steps, (k+1)*n/steps).
		from, to := k*n/steps, (k+1)*n/steps
		// Missing sample j is v-(j+1)*interval; record the step at its mean.
		value := v - (from+to+1)*interval/2
		_ = h.RecordValues(max(1, min(maxLatency, value)), to-from)
	}
}
func (c *Counters) RecordPublishLatency(d time.Duration) {
	c.RecordPublishLatencyFor(int(c.nextShard.Add(1)), d)
}
func (c *Counters) RecordPublishLatencyFor(shard int, d time.Duration) { c.record(shard, 0, d) }

// RecordPublishLatencyCorrected records a publish that was scheduled every
// interval; a publish slower than the interval also accounts for the slots
// that waited behind it, so stalls are not hidden from the percentiles.
func (c *Counters) RecordPublishLatencyCorrected(shard int, d, interval time.Duration) {
	c.recordCorrected(shard, 0, d, interval)
}
func (c *Counters) RecordEndToEndLatency(d time.Duration) {
	c.RecordEndToEndLatencyFor(int(c.nextShard.Add(1)), d)
}
func (c *Counters) RecordEndToEndLatencyFor(shard int, d time.Duration) {
	c.record(shard, 1, d)
	c.CorrelationSamples.Add(1)
}
func (c *Counters) RecordConnectionLatencyFor(shard int, d time.Duration) { c.record(shard, 2, d) }

type Snapshot struct {
	ConnectAttempts     uint64            `json:"connectAttempts"`
	Connected           int64             `json:"connected"`
	TargetClients       int64             `json:"targetClients"`
	Connecting          int64             `json:"connecting"`
	PeakConnected       int64             `json:"peakConnected"`
	ConnectErrors       uint64            `json:"connectErrors"`
	ConnectCancelled    uint64            `json:"connectCancelled"`
	PublishAttempts     uint64            `json:"publishAttempts"`
	Published           uint64            `json:"published"`
	PublishErrors       uint64            `json:"publishErrors"`
	PublishCancelled    uint64            `json:"publishCancelled"`
	BytesSent           uint64            `json:"bytesSent"`
	Received            uint64            `json:"received"`
	BytesReceived       uint64            `json:"bytesReceived"`
	Subscriptions       uint64            `json:"subscriptions"`
	SubscribeErrors     uint64            `json:"subscribeErrors"`
	CorrelationSamples  uint64            `json:"correlationSamples"`
	Reconnects          uint64            `json:"reconnects"`
	Churned             uint64            `json:"churned,omitempty"`
	ChurnReconnects     uint64            `json:"churnReconnects,omitempty"`
	Disconnects         uint64            `json:"disconnects"`
	PublishSlotsMissed  uint64            `json:"publishSlotsMissed"`
	CorrelationRejected uint64            `json:"correlationRejected"`
	Errors              map[string]uint64 `json:"errors,omitempty"`
	ConnectFailures     map[string]uint64 `json:"connectFailures,omitempty"`
	ConnackReasons      map[string]uint64 `json:"connackReasons,omitempty"`
	RemoteAddresses     []LabelCount      `json:"remoteAddresses,omitempty"`
	AddressLabels       []LabelCount      `json:"addressLabels,omitempty"`
	DNSAnswers          []LabelCount      `json:"dnsAnswers,omitempty"`
	DNSErrors           []LabelCount      `json:"dnsErrors,omitempty"`
	ConnectP50          time.Duration     `json:"connectP50"`
	ConnectP95          time.Duration     `json:"connectP95"`
	ConnectP99          time.Duration     `json:"connectP99"`
	EndToEndP95         time.Duration     `json:"endToEndP95"`
	EndToEndP50         time.Duration     `json:"endToEndP50"`
	EndToEndP99         time.Duration     `json:"endToEndP99"`
	PublishLatencyCount uint64            `json:"publishLatencyCount"`
	PublishLatencySum   time.Duration     `json:"publishLatencySum"`
	Min                 time.Duration     `json:"min"`
	Max                 time.Duration     `json:"max"`
	Avg                 time.Duration     `json:"avg"`
	Histogram           []Bucket          `json:"histogram"`
	P50                 time.Duration     `json:"p50"`
	P75                 time.Duration     `json:"p75"`
	P90                 time.Duration     `json:"p90"`
	P95                 time.Duration     `json:"p95"`
	P99                 time.Duration     `json:"p99"`
	P999                time.Duration     `json:"p999"`
}
type Bucket struct {
	UpperBoundNs time.Duration `json:"upperBoundNs"`
	Count        uint64        `json:"count"`
}

func (c *Counters) Snapshot() Snapshot {
	s := Snapshot{ConnectAttempts: c.ConnectAttempts.Load(), Connected: c.Connected.Load(), TargetClients: c.TargetClients.Load(), Connecting: c.Connecting.Load(), PeakConnected: c.PeakConnected.Load(), ConnectErrors: c.ConnectErrors.Load(), PublishAttempts: c.PublishAttempts.Load(), Published: c.Published.Load(), PublishErrors: c.PublishErrors.Load(), BytesSent: c.BytesSent.Load(), Received: c.Received.Load(), BytesReceived: c.BytesReceived.Load(), Subscriptions: c.Subscriptions.Load(), SubscribeErrors: c.SubscribeErrors.Load(), CorrelationSamples: c.CorrelationSamples.Load(), Reconnects: c.Reconnects.Load(), Churned: c.Churned.Load(), ChurnReconnects: c.ChurnReconnects.Load(), Disconnects: c.Disconnects.Load(), Errors: map[string]uint64{}}
	s.ConnectCancelled = c.ConnectCancelled.Load()
	s.PublishCancelled = c.PublishCancelled.Load()
	s.PublishSlotsMissed = c.PublishSlotsMissed.Load()
	s.CorrelationRejected = c.CorrelationRejected.Load()
	for i, name := range errorCategories {
		if n := c.errors[i].Load(); n > 0 {
			s.Errors[name] = n
		}
	}
	for i, name := range connectStages {
		if n := c.connectFailures[i].Load(); n > 0 {
			if s.ConnectFailures == nil {
				s.ConnectFailures = map[string]uint64{}
			}
			s.ConnectFailures[name] = n
		}
	}
	for code := range c.connackReasons {
		if n := c.connackReasons[code].Load(); n > 0 {
			if s.ConnackReasons == nil {
				s.ConnackReasons = map[string]uint64{}
			}
			s.ConnackReasons[strconv.Itoa(code)] = n
		}
	}
	c.fillLabels(&s)
	p, e, k, summary := c.merged()
	s.PublishLatencyCount = summary.count
	s.PublishLatencySum = summary.sum
	s.Min = summary.min
	s.Max = summary.max
	if summary.count > 0 {
		s.Avg = time.Duration(int64(summary.sum) / int64(summary.count))
	}
	percentiles(&s, p, e, k)
	return s
}
func (c *Counters) merged() (*hdr.Histogram, *hdr.Histogram, *hdr.Histogram, latencySummary) {
	p, e, k := histogram(), histogram(), histogram()
	var summary latencySummary
	for i := range c.shards {
		s := &c.shards[i]
		s.mu.Lock()
		if s.publish != nil {
			p.Merge(s.publish)
		}
		if s.endToEnd != nil {
			e.Merge(s.endToEnd)
		}
		if s.connection != nil {
			k.Merge(s.connection)
		}
		summary.count += s.summary.count
		summary.sum = saturateDuration(summary.sum, s.summary.sum)
		if s.summary.min > 0 && (summary.min == 0 || s.summary.min < summary.min) {
			summary.min = s.summary.min
		}
		summary.max = max(summary.max, s.summary.max)
		s.mu.Unlock()
	}
	return p, e, k, summary
}
func percentiles(s *Snapshot, p, e, k *hdr.Histogram) {
	s.P50 = time.Duration(p.ValueAtQuantile(50)) * time.Microsecond
	s.P75 = time.Duration(p.ValueAtQuantile(75)) * time.Microsecond
	s.P90 = time.Duration(p.ValueAtQuantile(90)) * time.Microsecond
	s.P95 = time.Duration(p.ValueAtQuantile(95)) * time.Microsecond
	s.P99 = time.Duration(p.ValueAtQuantile(99)) * time.Microsecond
	s.P999 = time.Duration(p.ValueAtQuantile(99.9)) * time.Microsecond
	s.EndToEndP95 = time.Duration(e.ValueAtQuantile(95)) * time.Microsecond
	s.EndToEndP50 = time.Duration(e.ValueAtQuantile(50)) * time.Microsecond
	s.EndToEndP99 = time.Duration(e.ValueAtQuantile(99)) * time.Microsecond
	s.Histogram = latencyBuckets(p)
	s.ConnectP50 = time.Duration(k.ValueAtQuantile(50)) * time.Microsecond
	s.ConnectP95 = time.Duration(k.ValueAtQuantile(95)) * time.Microsecond
	s.ConnectP99 = time.Duration(k.ValueAtQuantile(99)) * time.Microsecond
}
func latencyBuckets(h *hdr.Histogram) []Bucket {
	bounds := []time.Duration{time.Microsecond, 2 * time.Microsecond, 5 * time.Microsecond, 10 * time.Microsecond, 20 * time.Microsecond, 50 * time.Microsecond, 100 * time.Microsecond, 200 * time.Microsecond, 500 * time.Microsecond, time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 500 * time.Millisecond, time.Second, 10 * time.Second, 10 * time.Minute}
	bins := make([]Bucket, len(bounds))
	for i, b := range bounds {
		bins[i].UpperBoundNs = b
	}
	for _, bar := range h.Distribution() {
		if bar.Count <= 0 {
			continue
		}
		i := 0
		// Bucket by the bar's upper edge: a 5.001ms sample is not <= 5ms.
		for i < len(bounds)-1 && time.Duration(bar.To)*time.Microsecond > bounds[i] {
			i++
		}
		bins[i].Count += uint64(bar.Count)
	}
	return bins
}

// Distribution has bounded HDR buckets and permits exact merged percentiles.
type Distribution struct {
	Publish    *hdr.Snapshot `json:"publish"`
	EndToEnd   *hdr.Snapshot `json:"endToEnd"`
	Connection *hdr.Snapshot `json:"connection,omitempty"`
}

func ValidateDistribution(d Distribution) error {
	for _, s := range []*hdr.Snapshot{d.Publish, d.EndToEnd, d.Connection} {
		if s == nil {
			continue
		}
		if s.LowestTrackableValue != histogramShape.LowestTrackableValue || s.HighestTrackableValue != histogramShape.HighestTrackableValue || s.SignificantFigures != histogramShape.SignificantFigures || len(s.Counts) != len(histogramShape.Counts) {
			return errors.New("invalid HDR distribution shape")
		}
		var total int64
		for _, n := range s.Counts {
			if n < 0 || n > math.MaxInt64-total {
				return errors.New("invalid HDR bucket count")
			}
			total += n
		}
	}
	return nil
}
func (c *Counters) ExportDistribution() Distribution {
	p, e, k, _ := c.merged()
	return Distribution{Publish: p.Export(), EndToEnd: e.Export(), Connection: k.Export()}
}
func MergeSnapshots(snapshots []Snapshot, distributions []Distribution) Snapshot {
	out := Snapshot{Errors: map[string]uint64{}}
	for _, s := range snapshots {
		out.ConnectAttempts += s.ConnectAttempts
		out.Connected += s.Connected
		out.TargetClients += s.TargetClients
		out.Connecting += s.Connecting
		out.PeakConnected += s.PeakConnected
		out.ConnectErrors += s.ConnectErrors
		out.ConnectCancelled += s.ConnectCancelled
		out.PublishAttempts += s.PublishAttempts
		out.Published += s.Published
		out.PublishErrors += s.PublishErrors
		out.PublishCancelled += s.PublishCancelled
		out.BytesSent += s.BytesSent
		out.Received += s.Received
		out.BytesReceived += s.BytesReceived
		out.Subscriptions += s.Subscriptions
		out.SubscribeErrors += s.SubscribeErrors
		out.CorrelationSamples += s.CorrelationSamples
		out.Reconnects += s.Reconnects
		out.Churned += s.Churned
		out.ChurnReconnects += s.ChurnReconnects
		out.Disconnects += s.Disconnects
		out.PublishSlotsMissed += s.PublishSlotsMissed
		out.CorrelationRejected += s.CorrelationRejected
		out.PublishLatencyCount += s.PublishLatencyCount
		out.PublishLatencySum = saturateDuration(out.PublishLatencySum, s.PublishLatencySum)
		if s.Min > 0 && (out.Min == 0 || s.Min < out.Min) {
			out.Min = s.Min
		}
		out.Max = max(out.Max, s.Max)
		for _, name := range errorCategories {
			if n := s.Errors[name]; n > 0 {
				out.Errors[name] += n
			}
		}
		out.ConnectFailures = mergeStages(out.ConnectFailures, s.ConnectFailures)
		out.ConnackReasons = mergeCodes(out.ConnackReasons, s.ConnackReasons)
		out.RemoteAddresses = mergeLabelCounts(out.RemoteAddresses, s.RemoteAddresses)
		out.AddressLabels = mergeLabelCounts(out.AddressLabels, s.AddressLabels)
		out.DNSAnswers = mergeLabelCounts(out.DNSAnswers, s.DNSAnswers)
		out.DNSErrors = mergeLabelCounts(out.DNSErrors, s.DNSErrors)
	}
	p, e, k := histogram(), histogram(), histogram()
	for _, d := range distributions {
		if ValidateDistribution(d) != nil {
			continue
		}
		if d.Publish != nil {
			p.Merge(hdr.Import(d.Publish))
		}
		if d.EndToEnd != nil {
			e.Merge(hdr.Import(d.EndToEnd))
		}
		if d.Connection != nil {
			k.Merge(hdr.Import(d.Connection))
		}
	}
	percentiles(&out, p, e, k)
	if out.PublishLatencyCount > 0 {
		out.Avg = time.Duration(int64(out.PublishLatencySum) / int64(out.PublishLatencyCount))
	}
	return out
}

// mergeStages adds src into dst over the known stages only; unknown keys from
// untrusted worker reports fold into "other" so the sum stays equal to ConnectErrors.
func mergeStages(dst, src map[string]uint64) map[string]uint64 {
	other := connectStages[len(connectStages)-1]
	for k, n := range src {
		if n == 0 {
			continue
		}
		known := false
		for _, name := range connectStages {
			if name == k {
				known = true
				break
			}
		}
		if !known {
			k = other
		}
		if dst == nil {
			dst = map[string]uint64{}
		}
		dst[k] += n
	}
	return dst
}

// ParseConnackCode accepts only decimal CONNACK codes 0-255.
func ParseConnackCode(key string) (int, bool) {
	v, err := strconv.Atoi(key)
	return v, err == nil && v >= 0 && v <= 255
}

// mergeCodes adds src into dst, dropping keys that are not valid CONNACK codes.
func mergeCodes(dst, src map[string]uint64) map[string]uint64 {
	for k, n := range src {
		v, ok := ParseConnackCode(k)
		if !ok || n == 0 {
			continue
		}
		if dst == nil {
			dst = map[string]uint64{}
		}
		dst[strconv.Itoa(v)] += n
	}
	return dst
}
