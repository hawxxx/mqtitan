package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/emqx"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
	"log/slog"
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

// SetTelemetryAllowedHosts sets the hosts API-submitted scenarios may poll
// (comma-separated host or host:port, from MQTITAN_TELEMETRY_ALLOWED_HOSTS).
func (m *Manager) SetTelemetryAllowedHosts(list string) {
	m.mu.Lock()
	m.telemetryHosts = emqx.ParseHostList(list)
	m.mu.Unlock()
}

// checkTelemetryHosts rejects API-submitted telemetry that targets a host the
// operator has not allowlisted, so submitted YAML cannot aim the controller's
// credentials or network position at arbitrary hosts.
func (m *Manager) checkTelemetryHosts(t scenario.Telemetry) error {
	m.mu.Lock()
	allowed := m.telemetryHosts
	m.mu.Unlock()
	urls := make([]string, 0, len(t.EMQX)+1)
	for _, e := range t.EMQX {
		urls = append(urls, e.URL)
	}
	if t.Prometheus != nil {
		urls = append(urls, t.Prometheus.URL)
	}
	for _, u := range urls {
		if host, ok := emqx.HostAllowed(allowed, u); !ok {
			return fmt.Errorf("telemetry host %s is not in MQTITAN_TELEMETRY_ALLOWED_HOSTS", host)
		}
	}
	return nil
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
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	b, err := m.store.Get("brokers", id)
	if errors.Is(err, storage.ErrNotFound) {
		return []emqx.FleetSample{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []emqx.FleetSample
	if err = json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Manager) telemetryCreds(fromAPI bool) emqx.Credentials {
	m.mu.Lock()
	defer m.mu.Unlock()
	creds := m.creds
	if fromAPI {
		creds.AllowedHosts = m.telemetryHosts
	}
	return creds
}

// fetchBaseline takes the baseline "first" sample. startTest calls it before
// any worker assignment exists and before the local engine starts, so the
// baseline never includes the test's own connections.
func (m *Manager) fetchBaseline(ctx context.Context, t scenario.Telemetry, fromAPI bool) *emqx.FleetSample {
	if t.Baseline != "first" {
		return nil
	}
	s := emqx.FetchFleet(ctx, t, m.telemetryCreds(fromAPI), nil)
	s.Baseline = true
	return &s
}

// startTelemetry polls the scenario's telemetry source on the controller. A
// non-nil first is the baseline sample taken by fetchBaseline; later samples
// subtract it. The returned function stops the poller and waits for it.
func (m *Manager) startTelemetry(ctx context.Context, r *execution, t scenario.Telemetry, first *emqx.FleetSample) func() {
	creds := m.telemetryCreds(r.fromAPI)
	m.mu.Lock()
	every := m.telemetryEvery
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
		if level, message := telemetryChange(r.lastBroker, s); message != "" {
			m.logEvent(r, level, "%s", message)
		}
		r.lastBroker = &s
		m.mu.Unlock()
	}
	if first != nil {
		baseline = emqx.NewBaseline(*first)
		record(*first)
	}
	pctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for immediate := first == nil; ; immediate = false {
			if !immediate {
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
	return func() {
		cancel()
		wg.Wait()
		m.mu.Lock()
		b, err := json.Marshal(r.brokers)
		m.mu.Unlock()
		if err == nil {
			err = m.store.Put("brokers", r.test.ID, b)
		}
		if err != nil {
			slog.Error("broker sample persistence failed", "test", r.test.ID)
		}
	}
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
