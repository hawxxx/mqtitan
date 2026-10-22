package api

import (
	"context"
	"encoding/json"
	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"net/http"
	"sync"
	"time"
)

// maxBrokerSamples bounds the samples kept per test (12 hours at 15 s).
const maxBrokerSamples = 2880

// SetCredentials supplies the EMQX API and Prometheus credentials used by
// per-test telemetry. They come from controller environment variables.
func (m *Manager) SetCredentials(c emqx.Credentials) {
	m.mu.Lock()
	m.creds = c
	m.mu.Unlock()
}

// Brokers returns the telemetry samples of a test: live ones while it runs,
// the persisted ones afterwards.
func (m *Manager) Brokers(id string) ([]emqx.FleetSample, error) {
	m.mu.Lock()
	if r := m.runs[id]; r != nil {
		out := append([]emqx.FleetSample{}, r.brokers...)
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()
	b, err := m.store.Get("tests", id)
	if err != nil {
		return nil, err
	}
	var t Test
	if err = json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	if t.Brokers == nil {
		t.Brokers = []emqx.FleetSample{}
	}
	return t.Brokers, nil
}

// startTelemetry polls the scenario's telemetry source on the controller. With
// baseline "first" it takes one synchronous sample before returning, so the
// caller can start the engine afterwards. The returned function stops the
// poller and waits for it.
func (m *Manager) startTelemetry(ctx context.Context, r *execution, t scenario.Telemetry) func() {
	m.mu.Lock()
	creds, every := m.creds, m.telemetryEvery
	m.mu.Unlock()
	if every <= 0 {
		every = t.PollInterval()
	}
	var baseline emqx.Baseline
	record := func(s emqx.FleetSample) {
		m.mu.Lock()
		if len(r.brokers) >= maxBrokerSamples {
			r.brokers = append(r.brokers[:0], r.brokers[1:]...)
		}
		r.brokers = append(r.brokers, s)
		m.notify(r, Event{"broker", s})
		m.mu.Unlock()
	}
	if t.Baseline == "first" {
		s := emqx.FetchFleet(ctx, t, creds, nil)
		s.Baseline = true
		baseline = emqx.NewBaseline(s)
		record(s)
	}
	pctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for first := t.Baseline != "first"; ; first = false {
			if !first {
				select {
				case <-pctx.Done():
					return
				case <-ticker.C:
				}
			}
			s := emqx.FetchFleet(pctx, t, creds, nil)
			if pctx.Err() != nil {
				return
			}
			record(baseline.Apply(s))
		}
	}()
	return func() { cancel(); wg.Wait() }
}

type BrokerTelemetry struct {
	Configured bool           `json:"configured"`
	Status     string         `json:"status"`
	Snapshot   *emqx.Snapshot `json:"snapshot,omitempty"`
	Error      string         `json:"error,omitempty"`
}

func (m *Manager) ConfigureEMQX(client *emqx.Client) {
	if client == nil {
		return
	}
	m.mu.Lock()
	m.broker = BrokerTelemetry{Configured: true, Status: "unavailable"}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		_ = client.Poll(m.ctx, 5*time.Second, func(s emqx.Snapshot, err error) {
			v := BrokerTelemetry{Configured: true, Status: "ok", Snapshot: &s}
			if err != nil {
				v.Status = "unavailable"
				v.Error = "EMQX API unavailable; load tests continue independently"
			}
			m.mu.Lock()
			m.broker = v
			m.mu.Unlock()
		})
	}()
}
func (m *Manager) brokerRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tests/{id}/brokers", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Brokers(r.PathValue("id"))
		if err != nil {
			resultError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/v1/brokers/emqx", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		v := m.broker
		m.mu.Unlock()
		if !v.Configured {
			v.Status = "disabled"
		}
		writeJSON(w, 200, v)
	})
}
