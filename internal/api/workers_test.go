package api

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/platform"
)

func register(t *testing.T, r *workerRegistry, id string) string {
	t.Helper()
	session, err := r.register(Worker{ID: id, CPUCount: 4, SupportsLiveControl: true})
	if err != nil {
		t.Fatal(err)
	}
	if session == "" {
		t.Fatal("registration issued no session")
	}
	return session
}

// assign gives the worker an assignment the way Manager.assign does.
func assign(r *workerRegistry, id, assignmentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers[id].assignment = &Assignment{ID: assignmentID, TestID: "test-" + assignmentID}
	r.workers[id].report = Report{}
}

func report(sequence, published uint64, done bool) *Report {
	return &Report{AssignmentID: "a1", Sequence: sequence, Done: done, Snapshot: metrics.Snapshot{Published: published, ConnectAttempts: 10}}
}

func TestWorkerLeaseExpiryIsVisible(t *testing.T) {
	r := newWorkerRegistry()
	register(t, r, "worker-a")
	r.mu.Lock()
	r.workers["worker-a"].LastSeen = time.Now().Add(-time.Minute)
	r.mu.Unlock()
	workers := r.list()
	if len(workers) != 1 || workers[0].Status != "lost" {
		t.Fatalf("dead worker counted healthy: %+v", workers)
	}
	if workers[0].Session != "" {
		t.Fatal("worker session leaked through the worker list")
	}
}

func TestWorkerIDsAndCapacityBound(t *testing.T) {
	r := newWorkerRegistry()
	for _, id := range []string{"", "../../etc/passwd", "spaces in id"} {
		if _, err := r.register(Worker{ID: id}); err == nil {
			t.Fatalf("accepted invalid worker id %q", id)
		}
	}
}

func TestHeartbeatRequiresTheIssuedSession(t *testing.T) {
	r := newWorkerRegistry()
	session := register(t, r, "worker-a")
	assign(r, "worker-a", "a1")
	// Knowing a listed worker ID must not be enough to receive its assignment.
	for _, forged := range []string{"", "guess"} {
		if _, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: forged}); !errors.Is(err, errMustRegister) {
			t.Fatalf("session %q: err = %v, want errMustRegister", forged, err)
		}
	}
	resp, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session})
	if err != nil || resp.Assignment == nil {
		t.Fatalf("owner heartbeat failed: %v", err)
	}
}

func TestLiveOrAssignedWorkerIDCannotBeTakenOver(t *testing.T) {
	r := newWorkerRegistry()
	session := register(t, r, "worker-a")
	if _, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.register(Worker{ID: "worker-a"}); err == nil {
		t.Fatal("a second process took over a live worker ID")
	}
	assign(r, "worker-a", "a1")
	if _, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session, Report: report(1, 500, true)}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.workers["worker-a"].LastSeen = time.Now().Add(-time.Minute)
	r.mu.Unlock()
	// Even after the lease expires, a restarted process must not reset the
	// finished partition of a test that is still running.
	if _, err := r.register(Worker{ID: "worker-a"}); err == nil {
		t.Fatal("re-registration replaced a worker assigned to a running test")
	}
	// The owning process may re-register and keeps its report.
	again, err := r.register(Worker{ID: "worker-a", Session: session})
	if err != nil || again != session {
		t.Fatalf("owner re-registration failed: %v", err)
	}
	r.mu.Lock()
	kept := r.workers["worker-a"].report
	r.mu.Unlock()
	if !kept.Done || kept.Snapshot.Published != 500 {
		t.Fatalf("finished report was reset: %+v", kept)
	}
}

func TestStaleReportIsIgnored(t *testing.T) {
	r := newWorkerRegistry()
	session := register(t, r, "worker-a")
	assign(r, "worker-a", "a1")
	if _, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session, Report: report(2, 900, true)}); err != nil {
		t.Fatal(err)
	}
	// Sequence 1 was sent earlier but processed later; counters equal, so the
	// backwards-counter check alone would accept it and clear Done.
	stale := report(1, 900, false)
	if _, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session, Report: stale}); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	got := r.workers["worker-a"].report
	r.mu.Unlock()
	if got.Sequence != 2 || !got.Done {
		t.Fatalf("stale report applied: %+v", got)
	}
}

func TestRejectedReportKeepsRegistration(t *testing.T) {
	r := newWorkerRegistry()
	session := register(t, r, "worker-a")
	assign(r, "worker-a", "a1")
	if _, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session, Report: report(1, 900, false)}); err != nil {
		t.Fatal(err)
	}
	_, err := r.heartbeat(Heartbeat{ID: "worker-a", Session: session, Report: report(2, 100, false)})
	if !errors.Is(err, errReportRejected) || errors.Is(err, errMustRegister) {
		t.Fatalf("err = %v, want errReportRejected only", err)
	}
}

func TestPruneForgetsLongLostIdleWorkers(t *testing.T) {
	r := newWorkerRegistry()
	register(t, r, "idle")
	register(t, r, "busy")
	assign(r, "busy", "a1")
	r.mu.Lock()
	for _, w := range r.workers {
		w.LastSeen = time.Now().Add(-time.Hour)
	}
	r.mu.Unlock()
	register(t, r, "new")
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers["idle"] != nil || r.workers["busy"] == nil {
		t.Fatal("prune removed the wrong workers")
	}
}

func TestDistributedPeakIsObservedNotSummed(t *testing.T) {
	m, _ := setup(t)
	r := &execution{workerIDs: []string{"a", "b"}}
	for _, id := range r.workerIDs {
		register(t, m.workers, id)
	}
	set := func(a, b int64) metrics.Snapshot {
		m.workers.mu.Lock()
		m.workers.workers["a"].report.Snapshot = metrics.Snapshot{Connected: a, PeakConnected: 100}
		m.workers.workers["b"].report.Snapshot = metrics.Snapshot{Connected: b, PeakConnected: 100}
		m.workers.mu.Unlock()
		return m.workerSnapshot(r)
	}
	set(100, 0)
	if got := set(0, 100).PeakConnected; got != 100 {
		t.Fatalf("peak = %d, want 100: peaks at different times must not add up", got)
	}
	if got := set(80, 90).PeakConnected; got != 170 {
		t.Fatalf("peak = %d, want 170", got)
	}
}

func TestLostWorkerHoldsNoLiveClients(t *testing.T) {
	m, _ := setup(t)
	r := &execution{workerIDs: []string{"a"}}
	register(t, m.workers, "a")
	m.workers.mu.Lock()
	w := m.workers.workers["a"]
	w.report.Snapshot = metrics.Snapshot{Connected: 50, Connecting: 5, TargetClients: 60, Published: 1000}
	w.LastSeen = time.Now().Add(-time.Minute)
	m.workers.mu.Unlock()
	s := m.workerSnapshot(r)
	if s.Connected != 0 || s.Connecting != 0 || s.TargetClients != 0 || s.Published != 1000 {
		t.Fatalf("lost worker snapshot = %+v", s)
	}
}

func TestLostRegistrationResponseCanBeRetried(t *testing.T) {
	r := newWorkerRegistry()
	register(t, r, "worker-a") // response lost: the worker never saw the session
	if _, err := r.register(Worker{ID: "worker-a"}); err != nil {
		t.Fatalf("retry after a lost registration response refused: %v", err)
	}
}

func TestPeakNeverBelowSingleWorkerPeak(t *testing.T) {
	m, _ := setup(t)
	r := &execution{workerIDs: []string{"a"}}
	register(t, m.workers, "a")
	m.workers.mu.Lock()
	// Connected and finished between two controller samples.
	m.workers.workers["a"].report = Report{Done: true, Snapshot: metrics.Snapshot{PeakConnected: 50, ConnectAttempts: 50}}
	m.workers.mu.Unlock()
	if got := m.workerSnapshot(r).PeakConnected; got != 50 {
		t.Fatalf("peak = %d, want 50", got)
	}
}

func TestHeartbeatWithFullLabelSetsFitsLimit(t *testing.T) {
	full := func(prefix string) []metrics.LabelCount {
		l := make([]metrics.LabelCount, metrics.MaxLabelKeys+1)
		for i := range l {
			l[i] = metrics.LabelCount{Key: fmt.Sprintf("%s%d:65535", prefix, i), Label: fmt.Sprintf("label-%0120d", i), Connects: uint64(i) * 7919, Active: int64(i)}
		}
		return l
	}
	c := metrics.New()
	for i := range 5000 {
		c.RecordPublishLatencyFor(i, time.Duration(i+1)*time.Microsecond)
		c.RecordEndToEndLatencyFor(i, time.Duration(i+1)*time.Microsecond)
		c.RecordConnectionLatencyFor(i, time.Duration(i+1)*time.Microsecond)
	}
	snap := c.Snapshot()
	snap.RemoteAddresses, snap.AddressLabels, snap.DNSAnswers, snap.DNSErrors = full("2001:db8::"), full("l"), full("2001:db8:1::"), full("2001:db8:2::")
	hb := Heartbeat{ID: "w1", Report: &Report{AssignmentID: "a1", Snapshot: snap, Distribution: c.ExportDistribution()}}
	var raw, z bytes.Buffer
	zw := gzip.NewWriter(&z)
	if err := json.NewEncoder(io.MultiWriter(&raw, zw)).Encode(hb); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if z.Len() >= 2<<20 || raw.Len() >= 8<<20 {
		t.Fatalf("compressed %d, decompressed %d exceed the 2 MiB / 8 MiB bounds", z.Len(), raw.Len())
	}
	t.Logf("compressed %d bytes, decompressed %d bytes", z.Len(), raw.Len())
}

func TestRegisterCleansSelfReportedHost(t *testing.T) {
	r := newWorkerRegistry()
	long := strings.Repeat("x", 300)
	if _, err := r.register(Worker{ID: "w", Host: &platform.Host{CPUModel: "EPYC\x1b[31m" + long, OS: "linux", CPUs: -4, CPULimit: -1}}); err != nil {
		t.Fatal(err)
	}
	h := r.list()[0].Host
	if strings.ContainsRune(h.CPUModel, '\x1b') || len([]rune(h.CPUModel)) != 128 || h.CPUs != 0 || h.CPULimit != 0 || h.OS != "linux" {
		t.Fatalf("host not cleaned: %+v", h)
	}
}
