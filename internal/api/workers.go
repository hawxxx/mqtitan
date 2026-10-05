package api

import (
	"errors"
	"github.com/mqtitan/mqtitan/internal/engine"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"net/http"
	"regexp"
	"sort"
	"sync"
	"time"
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
	assignment *Assignment
	report     Report
}
type Report struct {
	AssignmentID    string               `json:"assignmentId"`
	Snapshot        metrics.Snapshot     `json:"snapshot"`
	Distribution    metrics.Distribution `json:"distribution"`
	Done            bool                 `json:"done"`
	Error           string               `json:"error,omitempty"`
	ControlRevision uint64               `json:"controlRevision"`
}
type Heartbeat struct {
	ID          string    `json:"id"`
	MemoryBytes uint64    `json:"memoryBytes"`
	ClientTime  time.Time `json:"clientTime"`
	Report      *Report   `json:"report,omitempty"`
}
type HeartbeatResponse struct {
	Assignment *Assignment `json:"assignment,omitempty"`
	ServerTime time.Time   `json:"serverTime"`
}
type workerRegistry struct {
	mu      sync.Mutex
	workers map[string]*workerState
}

var workerID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func newWorkerRegistry() *workerRegistry {
	return &workerRegistry{workers: make(map[string]*workerState)}
}
func (r *workerRegistry) register(w Worker) error {
	if !workerID.MatchString(w.ID) {
		return errors.New("worker id must be 1-64 letters, numbers, underscores or hyphens")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.workers[w.ID]; !ok && len(r.workers) >= 256 {
		return errors.New("worker registry full (256 workers)")
	}
	if existing := r.workers[w.ID]; existing != nil && existing.assignment != nil && !existing.report.Done {
		return errors.New("worker already has an active assignment")
	}
	w.Status = "ready"
	w.LastSeen = time.Now().UTC()
	r.workers[w.ID] = &workerState{Worker: w}
	return nil
}
func (r *workerRegistry) list() []Worker {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Worker, 0, len(r.workers))
	for _, s := range r.workers {
		w := s.Worker
		if time.Since(w.LastSeen) > 15*time.Second {
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
	if s == nil {
		return HeartbeatResponse{}, errors.New("worker must register")
	}
	now := time.Now().UTC()
	s.LastSeen = now
	s.MemoryBytes = h.MemoryBytes
	s.ClockOffsetMS = float64(now.Sub(h.ClientTime).Microseconds()) / 1000
	if h.Report != nil && s.assignment != nil && h.Report.AssignmentID == s.assignment.ID {
		var latest uint64
		if s.assignment.Load != nil {
			latest = s.assignment.Load.Revision
		}
		if h.Report.ControlRevision > latest {
			return HeartbeatResponse{}, errors.New("worker acknowledged an unknown live load revision")
		}
		if err := metrics.ValidateDistribution(h.Report.Distribution); err != nil {
			return HeartbeatResponse{}, errors.New("invalid latency histogram report")
		}
		if h.Report.Snapshot.Published < s.report.Snapshot.Published || h.Report.Snapshot.ConnectAttempts < s.report.Snapshot.ConnectAttempts {
			return HeartbeatResponse{}, errors.New("worker cumulative counters moved backwards")
		}
		s.report = *h.Report
		s.Connected = h.Report.Snapshot.Connected
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
		if err := m.workers.register(worker); err != nil {
			writeError(w, 409, "REGISTRATION_FAILED", err.Error())
			return
		}
		writeJSON(w, 201, map[string]string{"status": "registered"})
	})
	mux.HandleFunc("POST /api/v1/workers/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var body Heartbeat
		if !input(w, r, &body) {
			return
		}
		v, err := m.workers.heartbeat(body)
		if err != nil {
			writeError(w, 409, "WORKER_UNKNOWN", err.Error())
			return
		}
		writeJSON(w, 200, v)
	})
}
