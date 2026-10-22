package emqx

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

const (
	maxFleetConcurrency = 16
	maxFleetTimeout     = 5 * time.Second
	maxResponseBytes    = 1 << 20
	maxErrorText        = 200
)

// TargetSample is one broker target's connection count at a point in time.
type TargetSample struct {
	Target      string
	Zone        string
	Connections float64 // after baseline subtraction, clamped at 0
	Raw         float64 // as reported
	Error       string  `json:",omitempty"`
}

// FleetSample is one poll of every telemetry target. Source is "emqx" or
// "prometheus". Error describes source-level problems (query failure, fewer
// targets than expected); per-target problems are on the TargetSample.
type FleetSample struct {
	Timestamp time.Time
	Source    string
	Targets   []TargetSample
	Complete  bool
	Baseline  bool
	Error     string `json:",omitempty"`
}

// Credentials come from controller environment variables, never from YAML.
type Credentials struct{ APIKey, APISecret, PromToken, PromUser, PromPassword string }

// FetchFleet polls the single configured telemetry source. It never follows
// redirects, caps response sizes and bounds every request by min(interval/2, 5s).
// The HTTP client's transport is reused; hc may be nil.
func FetchFleet(ctx context.Context, t scenario.Telemetry, c Credentials, hc *http.Client) FleetSample {
	timeout := t.PollInterval() / 2
	if timeout > maxFleetTimeout {
		timeout = maxFleetTimeout
	}
	client := http.Client{}
	if hc != nil {
		client = *hc
	}
	client.Timeout = timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s := FleetSample{Timestamp: time.Now().UTC(), Targets: []TargetSample{}}
	if t.Prometheus != nil {
		s.Source = "prometheus"
		fetchPrometheus(ctx, &client, t, c, &s)
	} else {
		s.Source = "emqx"
		fetchEMQX(ctx, &client, t, c, &s)
	}
	s.Complete = s.Error == ""
	for _, ts := range s.Targets {
		if ts.Error != "" {
			s.Complete = false
		}
	}
	return s
}

func checkExpected(t scenario.Telemetry, s *FleetSample) {
	if t.ExpectedTargets > 0 && len(s.Targets) < t.ExpectedTargets && s.Error == "" {
		s.Error = fmt.Sprintf("%d of %d expected targets reported", len(s.Targets), t.ExpectedTargets)
	}
}

func fetchEMQX(ctx context.Context, hc *http.Client, t scenario.Telemetry, c Credentials, s *FleetSample) {
	s.Targets = make([]TargetSample, len(t.EMQX))
	sem := make(chan struct{}, maxFleetConcurrency)
	var wg sync.WaitGroup
	for i, tg := range t.EMQX {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ts := TargetSample{Target: tg.Name, Zone: tg.Zone}
			client := &Client{BaseURL: tg.URL, APIKey: c.APIKey, APISecret: c.APISecret, HTTPClient: hc, MaxResponseBytes: maxResponseBytes}
			snap, err := client.Fetch(ctx)
			// Fetch joins a metrics-endpoint error even when nodes succeeded.
			if snap.Connections != nil {
				ts.Raw, ts.Connections = *snap.Connections, *snap.Connections
			} else {
				ts.Error = "connections unavailable"
				if err != nil {
					ts.Error = truncate(err.Error())
				}
			}
			s.Targets[i] = ts
		}()
	}
	wg.Wait()
	checkExpected(t, s)
}

func truncate(v string) string {
	if len(v) > maxErrorText {
		return v[:maxErrorText]
	}
	return v
}

// Baseline holds each target's first raw value.
type Baseline map[string]float64

// NewBaseline records the raw value of every target that reported successfully.
func NewBaseline(s FleetSample) Baseline {
	b := Baseline{}
	for _, t := range s.Targets {
		if t.Error == "" && t.Target != "" {
			b[t.Target] = t.Raw
		}
	}
	return b
}

// Apply subtracts the baseline from each target, clamping at zero. Targets
// absent from the baseline count from 0. Raw is preserved.
func (b Baseline) Apply(s FleetSample) FleetSample {
	out := s
	out.Targets = make([]TargetSample, len(s.Targets))
	for i, t := range s.Targets {
		t.Connections = max(t.Raw-b[t.Target], 0)
		out.Targets[i] = t
	}
	return out
}
