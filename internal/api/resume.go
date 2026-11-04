package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/mqtitan/mqtitan/internal/engine"
	"github.com/mqtitan/mqtitan/internal/metrics"
)

// Resuming a distributed run
//
// The load lives in the workers, not the controller. With ResumeRuns the
// controller keeps an encrypted checkpoint of each distributed run (worker
// sessions and assignments), leaves the run "running" when it shuts down
// gracefully, and on the next start rebuilds the run from the checkpoint.
// Workers keep generating load while the controller is away (see the worker's
// --controller-grace), re-present their old session, and resend cumulative
// counters, so nothing is lost for workers that come back. Counters of a
// worker that does not come back are lost beyond the last periodic sample.

// resumeGrace is how long after a restart a worker may stay silent before its
// lease counts as expired, on top of workerLease.
const resumeGrace = 45 * time.Second

var errSuspended = errors.New("controller shut down; the run continues on its workers")

type checkpointWorker struct {
	Worker     Worker     `json:"worker"`
	Assignment Assignment `json:"assignment"`
}

type checkpoint struct {
	TestID       string             `json:"testId"`
	FromAPI      bool               `json:"fromApi"`
	Workers      []checkpointWorker `json:"workers"`
	Lost         []string           `json:"lost,omitempty"`
	Replacements []ReplacedWorker   `json:"replacements,omitempty"`
}

// saveCheckpoint stores the run's worker sessions and assignments.
func (m *Manager) saveCheckpoint(r *execution, fromAPI bool) {
	if !m.resume || len(r.workerIDs) == 0 {
		return
	}
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	m.saveCheckpointLocked(r, fromAPI)
}

// saveCheckpointLocked is saveCheckpoint with the registry lock held.
func (m *Manager) saveCheckpointLocked(r *execution, fromAPI bool) {
	if !m.resume || len(r.workerIDs) == 0 {
		return
	}
	cp := checkpoint{TestID: r.test.ID, FromAPI: fromAPI, Lost: slices.Clone(r.lostWorkers), Replacements: slices.Clone(r.replacements)}
	for _, id := range r.workerIDs {
		if w := m.workers.workers[id]; w != nil && w.assignment != nil {
			cp.Workers = append(cp.Workers, checkpointWorker{Worker: w.Worker, Assignment: *w.assignment})
		}
	}
	b, err := json.Marshal(cp)
	if err == nil {
		err = m.store.Put("checkpoints", cp.TestID, b)
	}
	if err != nil {
		slog.Error("run checkpoint failed", "test", cp.TestID)
	}
}

func (m *Manager) deleteCheckpoint(id string) {
	if m.resume {
		_ = m.store.Delete("checkpoints", id)
	}
}

// suspending reports whether a run that was cancelled should stay alive on its
// workers: the controller is shutting down and nobody asked this run to stop.
func (m *Manager) suspending(r *execution) bool {
	if !m.resume || r.stopRequested.Load() {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// interrupt marks a stored running test as interrupted.
func (m *Manager) interrupt(t Test, reason string) error {
	now := time.Now().UTC()
	t.Status = "interrupted"
	t.EndedAt = &now
	t.Error = reason
	if err := m.persist(t); err != nil {
		return err
	}
	noteInterrupted(m.store, t.ID, now)
	_ = m.store.Delete("checkpoints", t.ID)
	return nil
}

// Resume restarts the distributed runs found running at startup and marks the
// ones that cannot be resumed interrupted. Call it once, after the manager is
// fully configured.
func (m *Manager) Resume() {
	pending := m.pendingResume
	m.pendingResume = nil
	for _, t := range pending {
		if err := m.resumeRun(t); err != nil {
			slog.Warn("run could not be resumed", "test", t.ID, "error", err)
			if err := m.interrupt(t, "controller restarted before test completed"); err != nil {
				slog.Error("interrupted run could not be stored", "test", t.ID)
			}
		}
	}
}

func (m *Manager) resumeRun(t Test) error {
	b, err := m.store.Get("checkpoints", t.ID)
	if err != nil {
		return err
	}
	var cp checkpoint
	if err = json.Unmarshal(b, &cp); err != nil {
		return err
	}
	decode := decodeScenario
	if !cp.FromAPI {
		decode = decodeLocalScenario
	}
	s, err := decode(t.Scenario)
	if err != nil {
		return err
	}
	if err = s.ResolveRun(); err != nil {
		return err
	}
	if s, err = m.resolveCertificates(s); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	counters := metrics.New()
	r := &execution{fromAPI: cp.FromAPI, ctx: ctx, control: engine.NewControl(s.Clients.Count), test: t, cancel: cancel, counters: counters, subscribers: make(map[chan Event]struct{}), workerIDs: slices.Clone(t.WorkerIDs), snapshot: counters.Snapshot, stage: stageAt(s.Stages, time.Since(t.StartedAt)), maxLost: t.MaxLostWorkers, replaceLost: t.ReplaceLostWorkers, standby: slices.Clone(t.StandbyWorkers), lostWorkers: cp.Lost, replacements: cp.Replacements, peakConnected: t.Snapshot.PeakConnected}
	r.snapshot = func() metrics.Snapshot { return m.workerSnapshot(r) }
	if lc := t.LoadControl; lc != nil && lc.Revision > 0 {
		if err = r.control.Apply(engine.Load{Clients: lc.Clients, RatePerClient: lc.RatePerClient, Revision: lc.Revision}); err != nil {
			cancel()
			return err
		}
	}
	if raw, err := m.store.Get("events", t.ID); err == nil {
		_ = json.Unmarshal(raw, &r.events)
		if n := len(r.events); n > 0 {
			r.seq = r.events[n-1].Seq
		}
	}
	// Workers that do not return within resumeGrace+workerLease count as lost.
	seen := time.Now().UTC().Add(resumeGrace)
	m.workers.mu.Lock()
	for _, cw := range cp.Workers {
		w := cw.Worker
		a := cw.Assignment
		w.LastSeen = seen
		w.Status = "assigned"
		m.workers.workers[w.ID] = &workerState{Worker: w, assignment: &a, heartbeated: true}
	}
	m.workers.mu.Unlock()
	m.mu.Lock()
	m.runs[t.ID] = r
	m.logEvent(r, "warn", "Controller restarted; resuming the run with %d workers", len(r.workerIDs))
	m.wg.Add(1)
	m.mu.Unlock()
	go m.execute(ctx, r, s, nil)
	return nil
}
