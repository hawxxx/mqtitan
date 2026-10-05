package api

import (
	"github.com/mqtitan/mqtitan/internal/emqx"
	"net/http"
	"time"
)

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
