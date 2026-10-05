package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mqtitan/mqtitan/internal/storage"
)

var ErrRunNotFinished = errors.New("only finished tests can be run again")

// Rerun reads the encrypted original rather than the redacted browser/report copy.
// It creates a new execution; results and live overrides are never carried over.
func (m *Manager) Rerun(id string) (Test, error) {
	raw, err := m.store.Get("tests", id)
	if err != nil {
		return Test{}, err
	}
	var previous Test
	if err = json.Unmarshal(raw, &previous); err != nil {
		return Test{}, err
	}
	switch previous.Status {
	case "stopped", "completed", "failed", "interrupted":
	default:
		return Test{}, ErrRunNotFinished
	}
	return m.startTest(previous.Scenario, previous.WorkerIDs, previous.ID)
}

func (m *Manager) rerunRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/tests/{id}/rerun", func(w http.ResponseWriter, r *http.Request) {
		run, err := m.Rerun(r.PathValue("id"))
		switch {
		case err == nil:
			writeJSON(w, http.StatusCreated, run)
		case errors.Is(err, storage.ErrNotFound):
			resultError(w, err)
		case errors.Is(err, ErrBusy), errors.Is(err, ErrRunNotFinished):
			writeError(w, http.StatusConflict, "RERUN_CONFLICT", err.Error())
		default:
			// Validation may include stored secrets; do not echo its raw error.
			writeError(w, http.StatusBadRequest, "RERUN_UNAVAILABLE", "Unable to reuse this run. Review its scenario, certificate profiles and worker availability.")
		}
	})
}
