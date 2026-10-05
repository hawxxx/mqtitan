package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLiveLoadAPIValidatesAndRecordsChanges(t *testing.T) {
	m, h := setup(t)
	raw := strings.Replace(testScenario, "150ms", "5s", 1)
	run, err := m.Start(raw)
	if err != nil {
		t.Fatal(err)
	}
	route := "/api/v1/tests/" + run.ID + "/load"
	request := func(body string, token bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", route, strings.NewReader(body))
		if token {
			r.Header.Set("Authorization", "Bearer controller-secret")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(`{"clients":0,"ratePerClient":0,"revision":0}`, false); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	for _, body := range []string{`{"clients":2,"ratePerClient":1,"revision":0}`, `{"clients":-1,"ratePerClient":1,"revision":0}`, `{"clients":1,"ratePerClient":-1,"revision":0}`, `{"clients":1,"ratePerClient":1000001,"revision":0}`} {
		if w := request(body, true); w.Code != 400 {
			t.Fatalf("invalid load: %d", w.Code)
		}
	}
	w := request(`{"clients":0,"ratePerClient":0,"revision":0}`, true)
	if missing := request(`{}`, true); missing.Code != 400 {
		t.Fatalf("missing fields: %d", missing.Code)
	}
	if w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	updated, err := m.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LoadControl.Revision != 1 || !updated.LoadControl.Manual || len(updated.LoadChanges) != 1 {
		t.Fatal("load change not recorded")
	}
	if !strings.Contains(updated.Scenario, "ratePerClient: 1") {
		t.Fatal("original scenario mutated")
	}
	if w = request(`{"clients":1,"ratePerClient":1,"revision":0}`, true); w.Code != 409 {
		t.Fatalf("stale revision: %d", w.Code)
	}
	if w = request(`{"clients":1,"ratePerClient":1,"revision":1}`, true); w.Code != 429 {
		t.Fatalf("rapid command: %d", w.Code)
	}
	rawRecord, err := m.store.Get("tests", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Test
	if err = json.Unmarshal(rawRecord, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.LoadChanges) != 1 {
		t.Fatal("change history not durable")
	}
}

func TestDistributedLiveTargetsUseGlobalPartitionOffsets(t *testing.T) {
	m, _ := setup(t)
	for _, id := range []string{"a", "b"} {
		if err := m.workers.register(Worker{ID: id, SupportsLiveControl: true}); err != nil {
			t.Fatal(err)
		}
	}
	raw := strings.Replace(testScenario, "count: 1", "count: 4", 1)
	raw = strings.Replace(raw, "clients: 1", "clients: 4", 1)
	raw = strings.Replace(raw, "150ms", "5s", 1)
	run, err := m.StartOnWorkers(raw, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateLoad(run.ID, LoadRequest{Clients: 3, RatePerClient: 20}); err != nil {
		t.Fatal(err)
	}
	m.workers.mu.Lock()
	defer m.workers.mu.Unlock()
	for id, want := range map[string]int{"a": 2, "b": 1} {
		a := m.workers.workers[id].assignment
		if a.Load == nil || a.Load.Clients != want || a.Load.RatePerClient != 20 || a.Load.Revision != 1 {
			t.Fatal("incorrect distributed live partition")
		}
	}
	for _, w := range m.workers.workers {
		w.report.Done = true
	}
}
