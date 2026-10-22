package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
)

// LogEvent is one line of a test's run log: things the controller knows that
// counters cannot show, such as live load changes, lost workers and broker
// telemetry gaps. Messages never contain credentials or broker URLs.
type LogEvent struct {
	Seq     int       `json:"seq"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // info, action, warn, error
	Message string    `json:"message"`
}

const maxLogEvents = 1000

// logEvent records and streams an event. The caller holds m.mu.
func (m *Manager) logEvent(r *execution, level, format string, args ...any) {
	r.seq++
	e := LogEvent{Seq: r.seq, Time: time.Now().UTC(), Level: level, Message: fmt.Sprintf(format, args...)}
	if len(r.events) >= maxLogEvents {
		r.events = append(r.events[:0], r.events[1:]...)
	}
	r.events = append(r.events, e)
	m.notify(r, Event{"log", e})
}

// persistEvents stores the run log next to the test record. The caller must
// not hold m.mu.
func (m *Manager) persistEvents(r *execution) {
	m.mu.Lock()
	b, err := json.Marshal(r.events)
	m.mu.Unlock()
	if err == nil {
		err = m.store.Put("events", r.test.ID, b)
	}
	if err != nil {
		slog.Error("run log persistence failed", "test", r.test.ID)
	}
}

func (m *Manager) Events(id string) ([]LogEvent, error) {
	m.mu.Lock()
	if r := m.runs[id]; r != nil {
		out := append([]LogEvent(nil), r.events...)
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	b, err := m.store.Get("events", id)
	if errors.Is(err, storage.ErrNotFound) {
		return []LogEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LogEvent
	return out, json.Unmarshal(b, &out)
}

func (m *Manager) eventRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tests/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Events(r.PathValue("id"))
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
}

func startMessage(s scenario.Scenario, workerIDs []string) string {
	where := "the local engine"
	if len(workerIDs) > 0 {
		where = "workers " + strings.Join(workerIDs, ", ")
	}
	return fmt.Sprintf("Test %q started on %s: %d stage(s), capacity %d clients", s.Name, where, len(s.Stages), s.Clients.Count)
}

// stageAt returns the index of the stage running at elapsed, or -1 after the
// last stage.
func stageAt(stages []scenario.Stage, elapsed time.Duration) int {
	var end time.Duration
	for i, st := range stages {
		end += st.Duration.Duration
		if elapsed < end {
			return i
		}
	}
	return -1
}

func stageMessage(stages []scenario.Stage, i int) string {
	st := stages[i]
	verb := "hold"
	if i == 0 || stages[i-1].TargetClients != st.TargetClients {
		verb = "ramp to"
	}
	return fmt.Sprintf("Stage %d/%d: %s %d clients for %s", i+1, len(stages), verb, st.TargetClients, st.Duration.Duration)
}

// telemetryChange describes a change in broker telemetry health between two
// samples, or returns "" when nothing worth logging changed.
func telemetryChange(previous *emqx.FleetSample, s emqx.FleetSample) (level, message string) {
	problems := telemetryProblems(s)
	if len(problems) == 0 {
		if previous != nil && len(telemetryProblems(*previous)) > 0 {
			return "info", fmt.Sprintf("Broker telemetry complete again (%d targets reporting)", len(s.Targets))
		}
		return "", ""
	}
	if previous != nil && strings.Join(telemetryProblems(*previous), "\x00") == strings.Join(problems, "\x00") {
		return "", ""
	}
	return "warn", "Broker telemetry incomplete: " + strings.Join(problems, "; ")
}

func telemetryProblems(s emqx.FleetSample) []string {
	var out []string
	if s.Error != "" {
		out = append(out, s.Error)
	}
	for _, t := range s.Targets {
		if t.Error != "" {
			out = append(out, t.Target+": "+t.Error)
		}
	}
	return out
}
