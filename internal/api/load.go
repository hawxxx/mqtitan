package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/mqtitan/mqtitan/internal/engine"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
)

var (
	ErrLoadConflict = errors.New("live load changed or test is no longer running; refresh and retry")
	ErrLoadLimited  = errors.New("live updates require 250ms spacing and are limited to 1000 changes per test")
)

type LoadRequest struct {
	Clients       int     `json:"clients"`
	RatePerClient float64 `json:"ratePerClient"`
	Revision      uint64  `json:"revision"`
}

type LoadControl struct {
	CapacityClients  int      `json:"capacityClients"`
	MaxRatePerClient float64  `json:"maxRatePerClient"`
	HasPublishers    bool     `json:"hasPublishers"`
	Clients          int      `json:"clients"`
	RatePerClient    float64  `json:"ratePerClient"`
	Revision         uint64   `json:"revision"`
	Manual           bool     `json:"manual"`
	PendingWorkers   []string `json:"pendingWorkers,omitempty"`
}

type LoadChange struct {
	Timestamp     time.Time `json:"timestamp"`
	Clients       int       `json:"clients"`
	RatePerClient float64   `json:"ratePerClient"`
	Revision      uint64    `json:"revision"`
}

func initialLoad(s scenario.Scenario) *LoadControl {
	c := &LoadControl{CapacityClients: s.Clients.Count, MaxRatePerClient: engine.MaxLiveRate, Clients: s.Stages[0].TargetClients}
	for _, w := range s.Workloads {
		if w.Type == "publisher" || w.Type == "mixed" {
			if !c.HasPublishers {
				c.RatePerClient = w.RatePerClient
			}
			c.HasPublishers = true
		}
	}
	return c
}

func (m *Manager) UpdateLoad(id string, request LoadRequest) (Test, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil {
		if _, err := m.store.Get("tests", id); err != nil {
			return Test{}, err
		}
		return Test{}, ErrLoadConflict
	}
	if m.closed || r.test.Status != "running" || r.ctx.Err() != nil {
		return Test{}, ErrLoadConflict
	}
	current := r.test.LoadControl
	load := engine.Load{Clients: request.Clients, RatePerClient: request.RatePerClient, Revision: current.Revision + 1}
	if err := engine.ValidateLoad(load, current.CapacityClients); err != nil {
		return Test{}, err
	}
	if !current.HasPublishers && request.RatePerClient != 0 {
		return Test{}, errors.New("this scenario has no publishers; ratePerClient must be zero")
	}
	if request.Revision != current.Revision {
		return Test{}, ErrLoadConflict
	}
	now := time.Now().UTC()
	changes := r.test.LoadChanges
	if len(changes) >= 1000 || (len(changes) > 0 && now.Sub(changes[len(changes)-1].Timestamp) < 250*time.Millisecond) {
		return Test{}, ErrLoadLimited
	}
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	for _, id := range r.workerIDs {
		w := m.workers.workers[id]
		if w == nil || !w.SupportsLiveControl || time.Since(w.LastSeen) > 15*time.Second || w.assignment == nil || w.assignment.TestID != r.test.ID || w.report.Done {
			return Test{}, errors.New("assigned workers must be healthy and support live controls; upgrade workers if needed")
		}
	}
	updated := r.test
	next := *current
	next.Clients = load.Clients
	next.RatePerClient = load.RatePerClient
	next.Revision = load.Revision
	next.Manual = true
	updated.LoadControl = &next
	updated.LoadChanges = append(append([]LoadChange(nil), changes...), LoadChange{now, load.Clients, load.RatePerClient, load.Revision})
	// Persist before applying, so an unrecorded command can never change generated traffic.
	if err := m.persist(updated); err != nil {
		return Test{}, err
	}
	if err := r.control.Apply(load); err != nil {
		return Test{}, err
	}
	for _, id := range r.workerIDs {
		a := m.workers.workers[id].assignment
		partition := load
		partition.Clients = max(0, min(a.CapacityClients, load.Clients-a.ClientOffset))
		a.Load = &partition
	}
	r.test = updated
	response := updated
	responseControl := next
	responseControl.PendingWorkers = append([]string(nil), r.workerIDs...)
	response.LoadControl = &responseControl
	m.notify(r, Event{"status", publicTest(response)})
	return publicTest(response), nil
}

func (m *Manager) withLoadStatus(r *execution) Test {
	t := r.test
	if t.LoadControl == nil {
		return t
	}
	control := *t.LoadControl
	if !control.Manual {
		control.Clients = int(t.Snapshot.TargetClients)
	}
	if control.Revision > 0 && len(r.workerIDs) > 0 {
		m.workers.mu.Lock()
		control.PendingWorkers = nil
		for _, id := range r.workerIDs {
			if w := m.workers.workers[id]; w != nil && w.report.ControlRevision < control.Revision {
				control.PendingWorkers = append(control.PendingWorkers, id)
			}
		}
		m.workers.mu.Unlock()
	}
	t.LoadControl = &control
	return t
}

func (m *Manager) loadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tests/{id}/load", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Clients       *int     `json:"clients"`
			RatePerClient *float64 `json:"ratePerClient"`
			Revision      *uint64  `json:"revision"`
		}
		if !input(w, r, &body) {
			return
		}
		if body.Clients == nil || body.RatePerClient == nil || body.Revision == nil {
			writeError(w, 400, "INVALID_LOAD", "clients, ratePerClient and revision are required")
			return
		}
		test, err := m.UpdateLoad(r.PathValue("id"), LoadRequest{*body.Clients, *body.RatePerClient, *body.Revision})
		switch {
		case errors.Is(err, storage.ErrNotFound):
			resultError(w, err)
		case errors.Is(err, ErrLoadConflict):
			writeError(w, 409, "LOAD_CONFLICT", err.Error())
		case errors.Is(err, ErrLoadLimited):
			writeError(w, 429, "LOAD_LIMITED", err.Error())
		case err != nil:
			writeError(w, 400, "INVALID_LOAD", err.Error())
		default:
			writeJSON(w, 200, test)
		}
	})
}
