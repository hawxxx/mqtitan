package api

import (
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

// ReplacedWorker records that an idle worker took over a lost worker's partition.
type ReplacedWorker struct {
	Lost string    `json:"lost"`
	By   string    `json:"by"`
	At   time.Time `json:"at"`
}

// replacementStartDelay gives the replacement time to receive its assignment
// before the coordinated start.
const replacementStartDelay = 3 * time.Second

// maxReplacements bounds replacement churn, so a bad pool cannot cycle forever.
func maxReplacements(r *execution) int { return max(10, len(r.workerIDs)) }

// fastForward rewrites a partition scenario so a worker starting at newStart
// joins a run that began at origStart: stages that already ended are dropped
// and the current one keeps only its remaining time.
func fastForward(raw string, origStart, newStart time.Time) (string, error) {
	s, err := scenario.Parse([]byte(raw))
	if err != nil {
		return "", err
	}
	elapsed := max(0, newStart.Sub(origStart))
	var stages []scenario.Stage
	for _, st := range s.Stages {
		d := st.Duration.Duration
		if elapsed >= d {
			elapsed -= d
			continue
		}
		st.Duration.Duration = d - elapsed
		elapsed = 0
		stages = append(stages, st)
	}
	if len(stages) == 0 {
		return "", errors.New("the run is already past its last stage")
	}
	s.Stages = stages
	out, err := scenario.Encode(s)
	return string(out), err
}

// replaceLostLocked gives the lost worker's partition to an idle worker.
// It returns false when replacement is off, exhausted, or no worker is
// available, in which case the loss counts against maxLostWorkers. The
// caller holds the registry lock.
func (m *Manager) replaceLostLocked(r *execution, lostID string) (ReplacedWorker, bool) {
	if !r.replaceLost || len(r.replacements) >= maxReplacements(r) {
		return ReplacedWorker{}, false
	}
	lost := m.workers.workers[lostID]
	if lost == nil || lost.assignment == nil || lost.assignment.TestID != r.test.ID {
		return ReplacedWorker{}, false
	}
	orig := *lost.assignment
	now := time.Now().UTC()
	start := now.Add(replacementStartDelay)
	raw, err := fastForward(orig.Scenario, orig.StartAt, start)
	if err != nil {
		return ReplacedWorker{}, false
	}
	ids := make([]string, 0, len(m.workers.workers))
	for id := range m.workers.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := m.workers.workers[id]
		if slices.Contains(r.workerIDs, id) || w.assignment != nil || !w.heartbeated || now.Sub(w.LastSeen) > workerLease {
			continue
		}
		if len(r.standby) > 0 && !slices.Contains(r.standby, id) {
			continue
		}
		if orig.Load != nil && !w.SupportsLiveControl {
			continue
		}
		a := Assignment{ID: ID(), TestID: orig.TestID, Scenario: raw, StartAt: start, ClientOffset: orig.ClientOffset, CapacityClients: orig.CapacityClients}
		if orig.Load != nil {
			load := *orig.Load
			a.Load = &load
		}
		w.assignment = &a
		w.report = Report{}
		w.Status = "assigned"
		r.workerIDs = append(r.workerIDs, id)
		rep := ReplacedWorker{Lost: lostID, By: id, At: now}
		r.replacements = append(r.replacements, rep)
		return rep, true
	}
	return ReplacedWorker{}, false
}
