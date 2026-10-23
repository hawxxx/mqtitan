package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/distributed"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"go.opentelemetry.io/otel"
	"strings"
	"time"
)

func (m *Manager) assign(r *execution, s scenario.Scenario, ids []string) error {
	_, span := otel.Tracer("mqtitan/controller").Start(m.ctx, "worker.assign")
	defer span.End()
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range ids {
		w := m.workers.workers[id]
		if seen[id] || w == nil || time.Since(w.LastSeen) > workerLease || w.assignment != nil {
			return fmt.Errorf("worker %q is unavailable or duplicate", id)
		}
		seen[id] = true
	}
	assignments := make([]Assignment, len(ids))
	if s.SharedCorrelationID == "" {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return err
		}
		s.SharedCorrelationID = hex.EncodeToString(token)
	}
	start := time.Now().Add(3 * time.Second).UTC()
	for i, id := range ids {
		partition, err := distributed.Partition(s, i, len(ids))
		if err != nil {
			return err
		}
		partition.Clients.Worker = id
		raw, err := scenario.Encode(partition)
		if err != nil {
			return err
		}
		assignments[i] = Assignment{ID: ID(), TestID: r.test.ID, Scenario: string(raw), StartAt: start}
		assignments[i].ClientOffset = partition.Clients.SequenceOffset - s.Clients.SequenceOffset
		assignments[i].CapacityClients = partition.Clients.Count
	}
	for i, id := range ids {
		w := m.workers.workers[id]
		w.assignment = &assignments[i]
		w.report = Report{}
		w.Status = "assigned"
	}
	return nil
}
func (m *Manager) workerSnapshot(r *execution) metrics.Snapshot {
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	snapshots := make([]metrics.Snapshot, 0, len(r.workerIDs))
	distributions := make([]metrics.Distribution, 0, len(r.workerIDs))
	for _, id := range r.workerIDs {
		w := m.workers.workers[id]
		if w == nil {
			continue
		}
		s := w.report.Snapshot
		if time.Since(w.LastSeen) > workerLease || w.report.Done {
			// A lost or finished worker holds no live clients; its cumulative
			// counters and histograms still count toward the result.
			s.Connected, s.Connecting, s.TargetClients = 0, 0, 0
		}
		snapshots = append(snapshots, s)
		distributions = append(distributions, w.report.Distribution)
	}
	merged := metrics.MergeSnapshots(snapshots, distributions)
	// Per-worker peaks can occur at different times, so their sum overstates
	// concurrency. Report the highest aggregate the controller observed, which
	// is sampled and can miss a short burst, but never less than the largest
	// single-worker peak, which is a true lower bound.
	r.peakConnected = max(r.peakConnected, merged.Connected)
	for _, s := range snapshots {
		r.peakConnected = max(r.peakConnected, s.PeakConnected)
	}
	merged.PeakConnected = r.peakConnected
	return merged
}
func (m *Manager) stopAssignments(r *execution) {
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	for _, id := range r.workerIDs {
		if w := m.workers.workers[id]; w != nil && w.assignment != nil && w.assignment.TestID == r.test.ID {
			w.assignment.Stop = true
		}
	}
}
func (m *Manager) releaseAssignments(r *execution) {
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	for _, id := range r.workerIDs {
		if w := m.workers.workers[id]; w != nil && w.assignment != nil && w.assignment.TestID == r.test.ID {
			w.assignment = nil
			w.Status = "ready"
		}
	}
}

// drain asks every worker to stop and waits, up to ten seconds, for the live
// ones to send a final report, so their last counters reach the result.
func (m *Manager) drain(r *execution) {
	m.stopAssignments(r)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pending := false
		m.workers.mu.Lock()
		for _, id := range r.workerIDs {
			if w := m.workers.workers[id]; w != nil && !w.report.Done && time.Since(w.LastSeen) <= workerLease {
				pending = true
			}
		}
		m.workers.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func (m *Manager) runDistributed(ctx context.Context, r *execution) error {
	defer m.stopAssignments(r)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			m.drain(r)
			return ctx.Err()
		case <-tick.C:
			all := true
			var failure error
			var lost, failed []string
			m.workers.mu.Lock()
			for _, id := range r.workerIDs {
				w := m.workers.workers[id]
				if w == nil || (time.Since(w.LastSeen) > workerLease && !w.report.Done) {
					// A worker that already finished may go away safely.
					lost = append(lost, id)
					continue
				}
				if !w.report.Done {
					all = false
				}
				if w.report.Error != "" {
					failure = errors.New("worker execution failed")
					failed = append(failed, id)
				}
			}
			if len(lost) > 0 {
				r.lostWorkers = lost
				failure = fmt.Errorf("worker lease expired (%s); assigned load lost", strings.Join(lost, ", "))
			}
			m.workers.mu.Unlock()
			if failure != nil {
				m.mu.Lock()
				if len(failed) > 0 {
					m.logEvent(r, "error", "Worker execution failed on %s; stopping the other workers", strings.Join(failed, ", "))
				}
				if len(lost) > 0 {
					m.logEvent(r, "error", "Worker lease expired on %s; its assigned load is lost", strings.Join(lost, ", "))
				}
				m.mu.Unlock()
				// Automatic replacement is not implemented: stop the survivors
				// and collect their final counters before failing the run.
				m.drain(r)
				return failure
			}
			if all {
				return nil
			}
		}
	}
}
