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
		if seen[id] || w == nil || time.Since(w.LastSeen) > 15*time.Second || w.assignment != nil {
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
		if time.Since(w.LastSeen) > 15*time.Second {
			s.Connected = 0
		}
		snapshots = append(snapshots, s)
		distributions = append(distributions, w.report.Distribution)
	}
	return metrics.MergeSnapshots(snapshots, distributions)
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
func (m *Manager) runDistributed(ctx context.Context, r *execution) error {
	defer m.stopAssignments(r)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAssignments(r)
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				all := true
				m.workers.mu.Lock()
				for _, id := range r.workerIDs {
					w := m.workers.workers[id]
					if w != nil && !w.report.Done {
						all = false
					}
				}
				m.workers.mu.Unlock()
				if all {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			return ctx.Err()
		case <-tick.C:
			all := true
			var failure error
			m.workers.mu.Lock()
			for _, id := range r.workerIDs {
				w := m.workers.workers[id]
				if w == nil || time.Since(w.LastSeen) > 15*time.Second {
					failure = fmt.Errorf("worker %s lease expired; assigned load lost", id)
					break
				}
				if !w.report.Done {
					all = false
				}
				if w.report.Error != "" {
					failure = errors.New("worker execution failed")
				}
			}
			m.workers.mu.Unlock()
			if failure != nil {
				return failure
			}
			if all {
				return nil
			}
		}
	}
}
