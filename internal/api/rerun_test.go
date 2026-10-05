package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/storage"
)

func saveRerunSource(t *testing.T, m *Manager, status string, workers []string) Test {
	t.Helper()
	now := time.Now().UTC()
	previous := Test{ID: ID(), Name: "API test", Status: status, StartedAt: now.Add(-time.Minute), EndedAt: &now,
		Scenario: strings.Replace(testScenario, "150ms", "5s", 1), WorkerIDs: workers,
		Snapshot:    metrics.Snapshot{Published: 1234, PeakConnected: 1},
		LoadControl: &LoadControl{Clients: 0, RatePerClient: 0, Revision: 4, Manual: true},
		LoadChanges: []LoadChange{{Timestamp: now, Clients: 0, RatePerClient: 0, Revision: 4}}}
	if err := m.persist(previous); err != nil {
		t.Fatal(err)
	}
	return previous
}

func TestRerunCreatesFreshRunAndPreservesOriginalSecretsAndResults(t *testing.T) {
	for _, status := range []string{"stopped", "completed", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			m, _ := setup(t)
			previous := saveRerunSource(t, m, status, []string{})
			before, _ := m.store.Get("tests", previous.ID)
			sample, _ := json.Marshal(Sample{Timestamp: time.Now(), Snapshot: previous.Snapshot})
			if err := m.store.AddSample(previous.ID, time.Now().UnixNano(), sample); err != nil {
				t.Fatal(err)
			}
			run, err := m.Rerun(previous.ID)
			if err != nil {
				t.Fatal(err)
			}
			if run.ID == previous.ID || run.SourceTestID != previous.ID || run.Status != "running" || run.EndedAt != nil {
				t.Fatal("rerun did not create a distinct fresh execution")
			}
			if run.Snapshot.Published != 0 || run.LoadControl.Revision != 0 || run.LoadControl.Manual || len(run.LoadChanges) != 0 || len(run.Thresholds) != 0 {
				t.Fatal("rerun inherited results or manual overrides")
			}
			if strings.Contains(run.Scenario, "private-password") {
				t.Fatal("public rerun leaks password")
			}
			stored, err := m.store.Get("tests", run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var fresh Test
			if err = json.Unmarshal(stored, &fresh); err != nil {
				t.Fatal(err)
			}
			if fresh.Scenario != previous.Scenario {
				t.Fatal("original credentials/configuration were not retained server-side")
			}
			after, _ := m.store.Get("tests", previous.ID)
			if !bytes.Equal(before, after) {
				t.Fatal("original result mutated")
			}
			oldSamples, _ := m.Samples(previous.ID)
			newSamples, _ := m.Samples(run.ID)
			if len(oldSamples) != 1 || len(newSamples) != 0 {
				t.Fatal("samples copied or lost")
			}
		})
	}
}

func TestRerunHTTPAuthConflictsAndNotFound(t *testing.T) {
	m, h := setup(t)
	previous := saveRerunSource(t, m, "stopped", nil)
	request := func(id string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/tests/"+id+"/rerun", nil)
		if auth {
			r.Header.Set("Authorization", "Bearer controller-secret")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(previous.ID, false); w.Code != 401 {
		t.Fatalf("auth: %d", w.Code)
	}
	if w := request("missing", true); w.Code != 404 {
		t.Fatalf("missing: %d", w.Code)
	}
	active := saveRerunSource(t, m, "running", nil)
	if w := request(active.ID, true); w.Code != 409 {
		t.Fatalf("active source: %d", w.Code)
	}
	w := request(previous.ID, true)
	if w.Code != 201 || strings.Contains(w.Body.String(), "private-password") {
		t.Fatalf("rerun response: %d", w.Code)
	}
	var result struct {
		Data Test `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.SourceTestID != previous.ID {
		t.Fatal("lineage missing from response")
	}
	if w := request(previous.ID, true); w.Code != 409 {
		t.Fatalf("duplicate launch: %d", w.Code)
	}
}

func TestRerunPreservesDistributedWorkersAndNeverFallsBackWhenUnavailable(t *testing.T) {
	m, _ := setup(t)
	previous := saveRerunSource(t, m, "completed", []string{"worker-a"})
	if _, err := m.Rerun(previous.ID); err == nil {
		t.Fatal("unavailable worker silently fell back to local")
	}
	if err := m.workers.register(Worker{ID: "worker-a", SupportsLiveControl: true}); err != nil {
		t.Fatal(err)
	}
	run, err := m.Rerun(previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.WorkerIDs) != 1 || run.WorkerIDs[0] != "worker-a" {
		t.Fatal("worker selection lost")
	}
	m.workers.mu.Lock()
	w := m.workers.workers["worker-a"]
	assigned := w.assignment != nil && w.assignment.TestID == run.ID
	w.report.Done = true
	m.workers.mu.Unlock()
	if !assigned {
		t.Fatal("worker was not assigned the new run")
	}
}

func TestRerunAfterControllerRestartUsesEncryptedOriginal(t *testing.T) {
	m, _ := setup(t)
	previous := saveRerunSource(t, m, "stopped", []string{})
	m.Close()
	recovered, err := NewManager(m.store)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	run, err := recovered.Rerun(previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == previous.ID || run.SourceTestID != previous.ID {
		t.Fatal("restart rerun failed")
	}
	if _, err = recovered.Rerun("missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal("missing result not preserved")
	}
}
