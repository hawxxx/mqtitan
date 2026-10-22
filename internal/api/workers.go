package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/engine"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/platform"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Worker struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	CPUCount            int       `json:"cpuCount"`
	MemoryBytes         uint64    `json:"memoryBytes"`
	Connected           int64     `json:"connected"`
	LastSeen            time.Time `json:"lastSeen"`
	ClockOffsetMS       float64   `json:"clockOffsetMs"`
	SupportsLiveControl bool      `json:"supportsLiveControl"`
	// Host is self-reported at registration and only displayed.
	Host *platform.Host `json:"host,omitempty"`
	// Session is issued by the controller at registration and must accompany
	// every heartbeat. It is never listed, so another API caller cannot pose
	// as a registered worker and receive its assignment (with credentials).
	Session string `json:"session,omitempty"`
}
type Assignment struct {
	ID              string       `json:"id"`
	TestID          string       `json:"testId"`
	Scenario        string       `json:"scenario"`
	StartAt         time.Time    `json:"startAt"`
	Stop            bool         `json:"stop"`
	ClientOffset    int          `json:"clientOffset"`
	CapacityClients int          `json:"capacityClients"`
	Load            *engine.Load `json:"load,omitempty"`
}
type workerState struct {
	Worker
	assignment  *Assignment
	report      Report
	heartbeated bool
}
type Report struct {
	AssignmentID    string               `json:"assignmentId"`
	Snapshot        metrics.Snapshot     `json:"snapshot"`
	Distribution    metrics.Distribution `json:"distribution"`
	Done            bool                 `json:"done"`
	Error           string               `json:"error,omitempty"`
	ControlRevision uint64               `json:"controlRevision"`
	// Sequence increases with every report for an assignment. A report that
	// arrives after a newer one (a timed-out request processed late) is ignored.
	Sequence uint64 `json:"sequence,omitempty"`
}
type Heartbeat struct {
	ID          string    `json:"id"`
	MemoryBytes uint64    `json:"memoryBytes"`
	ClientTime  time.Time `json:"clientTime"`
	Report      *Report   `json:"report,omitempty"`
	Session     string    `json:"session,omitempty"`
}
type HeartbeatResponse struct {
	Assignment *Assignment `json:"assignment,omitempty"`
	ServerTime time.Time   `json:"serverTime"`
}
type workerRegistry struct {
	mu      sync.Mutex
	workers map[string]*workerState
}

// workerLease is how long a worker stays live without a heartbeat.
const workerLease = 15 * time.Second

var workerID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

var (
	// errMustRegister tells a worker its registration is gone (controller
	// restart) or now belongs to another process; it must register again.
	errMustRegister = errors.New("worker must register")
	// errReportRejected rejects one report without revoking the registration.
	errReportRejected = errors.New("worker report rejected")
)

func newWorkerRegistry() *workerRegistry {
	return &workerRegistry{workers: make(map[string]*workerState)}
}

// register returns the session the worker must present on heartbeats.
func (r *workerRegistry) register(w Worker) (string, error) {
	if !workerID.MatchString(w.ID) {
		return "", errors.New("worker id must be 1-64 letters, numbers, underscores or hyphens")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	r.prune(now)
	existing := r.workers[w.ID]
	if existing == nil && len(r.workers) >= 256 {
		return "", errors.New("worker registry full (256 workers)")
	}
	owner := existing != nil && w.Session != "" && subtle.ConstantTimeCompare([]byte(w.Session), []byte(existing.Session)) == 1
	if existing != nil && existing.assignment != nil {
		// The owning process may re-register (it lost a heartbeat response);
		// keep its assignment and report so a finished partition stays finished.
		if owner {
			existing.LastSeen = now
			return existing.Session, nil
		}
		// A restarted or duplicate process must not take over, or reset the
		// results of, a partition assigned to a running test.
		return "", errors.New("worker ID is assigned to a running test; wait for the test to finish or use another ID")
	}
	// An entry that never heartbeated belongs to a registration whose
	// response was lost; the retrying worker may replace it immediately.
	if existing != nil && !owner && existing.heartbeated && now.Sub(existing.LastSeen) <= workerLease {
		return "", errors.New("worker ID is in use by a live worker; use another ID")
	}
	w.Host = cleanHost(w.Host)
	w.Session = ID() + ID()
	w.Status = "ready"
	w.LastSeen = now
	r.workers[w.ID] = &workerState{Worker: w}
	return w.Session, nil
}

// prune forgets idle workers whose lease expired long ago, so replaced pods
// do not exhaust the registry. Workers with an assignment are kept.
func (r *workerRegistry) prune(now time.Time) {
	for id, s := range r.workers {
		if s.assignment == nil && now.Sub(s.LastSeen) > 10*workerLease {
			delete(r.workers, id)
		}
	}
}
func (r *workerRegistry) list() []Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Worker, 0, len(r.workers))
	for _, s := range r.workers {
		w := s.Worker
		w.Session = ""
		if time.Since(w.LastSeen) > workerLease {
			w.Status = "lost"
		} else if s.assignment != nil && !s.report.Done {
			w.Status = "running"
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (r *workerRegistry) heartbeat(h Heartbeat) (HeartbeatResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.workers[h.ID]
	if s == nil || subtle.ConstantTimeCompare([]byte(h.Session), []byte(s.Session)) != 1 {
		return HeartbeatResponse{}, errMustRegister
	}
	now := time.Now().UTC()
	s.LastSeen = now
	s.heartbeated = true
	s.MemoryBytes = h.MemoryBytes
	s.ClockOffsetMS = float64(now.Sub(h.ClientTime).Microseconds()) / 1000
	if h.Report != nil && s.assignment != nil && h.Report.AssignmentID == s.assignment.ID {
		var latest uint64
		if s.assignment.Load != nil {
			latest = s.assignment.Load.Revision
		}
		stale := h.Report.Sequence > 0 && s.report.AssignmentID == h.Report.AssignmentID && h.Report.Sequence <= s.report.Sequence
		switch {
		case stale:
			// Delivered after a newer report; applying it would move counters,
			// Done and the control acknowledgement backwards.
		case h.Report.ControlRevision > latest:
			return HeartbeatResponse{}, fmt.Errorf("%w: unknown live load revision", errReportRejected)
		case metrics.ValidateDistribution(h.Report.Distribution) != nil:
			return HeartbeatResponse{}, fmt.Errorf("%w: invalid latency histogram", errReportRejected)
		case h.Report.Snapshot.Published < s.report.Snapshot.Published || h.Report.Snapshot.ConnectAttempts < s.report.Snapshot.ConnectAttempts:
			return HeartbeatResponse{}, fmt.Errorf("%w: cumulative counters moved backwards", errReportRejected)
		default:
			s.report = *h.Report
			s.Connected = h.Report.Snapshot.Connected
		}
	}
	var a *Assignment
	if s.assignment != nil {
		copy := *s.assignment
		a = &copy
	}
	return HeartbeatResponse{a, now}, nil
}
func (m *Manager) workerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workers", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, m.workers.list()) })
	mux.HandleFunc("POST /api/v1/workers/register", func(w http.ResponseWriter, r *http.Request) {
		var worker Worker
		if !input(w, r, &worker) {
			return
		}
		session, err := m.workers.register(worker)
		if err != nil {
			writeError(w, 409, "REGISTRATION_FAILED", err.Error())
			return
		}
		writeJSON(w, 201, map[string]string{"status": "registered", "session": session})
	})
	mux.HandleFunc("POST /api/v1/workers/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var body Heartbeat
		if !input(w, r, &body) {
			return
		}
		v, err := m.workers.heartbeat(body)
		if errors.Is(err, errReportRejected) {
			writeError(w, 422, "REPORT_REJECTED", err.Error())
			return
		}
		if err != nil {
			writeError(w, 409, "WORKER_UNKNOWN", err.Error())
			return
		}
		writeJSON(w, 200, v)
	})
}

// cleanHost bounds self-reported host text: it is untrusted input shown to
// every operator, so control characters are dropped and lengths capped.
func cleanHost(h *platform.Host) *platform.Host {
	if h == nil {
		return nil
	}
	c := *h
	for _, f := range []*string{&c.Hostname, &c.OS, &c.Arch, &c.Distro, &c.Kernel, &c.CPUModel, &c.Container, &c.GoVersion, &c.Version} {
		*f = cleanText(*f, 128)
	}
	c.CPUs = max(0, min(c.CPUs, 1<<16))
	if c.CPULimit < 0 || c.CPULimit > 1<<16 {
		c.CPULimit = 0
	}
	return &c
}

func cleanText(s string, limit int) string {
	out := make([]rune, 0, min(len(s), limit))
	for _, r := range s {
		if len(out) == limit {
			break
		}
		if unicode.IsPrint(r) {
			out = append(out, r)
		}
	}
	return strings.TrimSpace(string(out))
}
