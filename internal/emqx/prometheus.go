package emqx

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mqtitan/mqtitan/internal/scenario"
)

const maxPrometheusSeries = 4096

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// fetchPrometheus runs an instant query. Errors are generic: they never carry
// credentials or response bodies.
func fetchPrometheus(ctx context.Context, hc *http.Client, t scenario.Telemetry, c Credentials, s *FleetSample) {
	p := t.Prometheus
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" || u.User != nil {
		s.Error = "invalid Prometheus URL"
		return
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/query"
	u.RawQuery = url.Values{"query": {p.Query}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		s.Error = "invalid Prometheus request"
		return
	}
	req.Header.Set("Accept", "application/json")
	if c.PromToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.PromToken)
	} else if c.PromUser != "" || c.PromPassword != "" {
		req.SetBasicAuth(c.PromUser, c.PromPassword)
	}
	resp, err := hc.Do(req)
	if err != nil {
		s.Error = "Prometheus request failed"
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.Error = "Prometheus HTTP status " + strconv.Itoa(resp.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		s.Error = "Prometheus response read failed"
		return
	}
	if len(body) > maxResponseBytes {
		s.Error = "Prometheus response too large"
		return
	}
	var pr promResponse
	if json.Unmarshal(body, &pr) != nil {
		s.Error = "invalid Prometheus response"
		return
	}
	if pr.Status != "success" || pr.Data.ResultType != "vector" {
		s.Error = "Prometheus query did not return an instant vector"
		return
	}
	targetLabel, zoneLabel := p.TargetLabel, p.ZoneLabel
	if targetLabel == "" {
		targetLabel = "pod"
	}
	if zoneLabel == "" {
		zoneLabel = "zone"
	}
	seen := map[string]bool{}
	for i, r := range pr.Data.Result {
		if i >= maxPrometheusSeries {
			s.Error = "Prometheus returned more than 4096 series"
			break
		}
		ts := TargetSample{Target: r.Metric[targetLabel], Zone: r.Metric[zoneLabel]}
		switch {
		case ts.Target == "":
			ts.Error = "missing label " + targetLabel
		case seen[ts.Target]:
			ts.Error = "duplicate series for target"
		default:
			seen[ts.Target] = true
			var text string
			if len(r.Value) != 2 || json.Unmarshal(r.Value[1], &text) != nil {
				ts.Error = "invalid sample value"
			} else if v, err := strconv.ParseFloat(text, 64); err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				ts.Error = "invalid sample value"
			} else {
				ts.Raw, ts.Connections = v, v
			}
		}
		s.Targets = append(s.Targets, ts)
	}
	checkExpected(t, s)
}
