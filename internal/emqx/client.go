// Package emqx provides optional, bounded broker observations independently of load execution.
package emqx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.opentelemetry.io/otel"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL          string
	APIKey           string
	APISecret        string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

type Node struct {
	Name            string   `json:"name"`
	Status          string   `json:"status,omitempty"`
	LiveConnections *float64 `json:"liveConnections,omitempty"`
	CPUUse          *float64 `json:"cpuUse,omitempty"`
	MemoryUsed      string   `json:"memoryUsed,omitempty"`
}

type Snapshot struct {
	Timestamp        time.Time          `json:"timestamp"`
	Nodes            []Node             `json:"nodes"`
	Metrics          map[string]float64 `json:"metrics"`
	Connections      *float64           `json:"connections,omitempty"`
	MessagesReceived *float64           `json:"messagesReceived,omitempty"`
	MessagesSent     *float64           `json:"messagesSent,omitempty"`
	MessagesDropped  *float64           `json:"messagesDropped,omitempty"`
}

// Fetch returns partial observations with an error when one endpoint is unavailable.
// Credentials and server error bodies are never included in errors.
func (c *Client) Fetch(ctx context.Context) (Snapshot, error) {
	ctx, span := otel.Tracer("mqtitan/emqx").Start(ctx, "emqx.poll")
	defer span.End()
	s := Snapshot{Timestamp: time.Now().UTC(), Nodes: []Node{}, Metrics: map[string]float64{}}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return s, errors.New("EMQX base URL must be an HTTP(S) origin without credentials")
	}
	if c.APIKey == "" || c.APISecret == "" {
		return s, errors.New("EMQX API key and secret are required")
	}
	var errs []error
	var nodes []map[string]json.RawMessage
	if err := c.get(ctx, strings.TrimRight(c.BaseURL, "/")+"/api/v5/nodes", &nodes); err != nil {
		errs = append(errs, fmt.Errorf("EMQX nodes: %w", err))
	} else {
		total := 0.0
		complete := len(nodes) > 0
		for _, raw := range nodes {
			n := Node{Name: textValue(raw["node"]), Status: textValue(raw["node_status"]), LiveConnections: number(raw["live_connections"]), CPUUse: number(raw["cpu_use"]), MemoryUsed: textValue(raw["memory_used"])}
			s.Nodes = append(s.Nodes, n)
			if n.LiveConnections == nil {
				complete = false
			} else {
				total += *n.LiveConnections
			}
		}
		if complete {
			s.Connections = &total
		}
	}
	var metrics []map[string]json.RawMessage
	if err := c.get(ctx, strings.TrimRight(c.BaseURL, "/")+"/api/v5/metrics?aggregate=false", &metrics); err != nil {
		errs = append(errs, fmt.Errorf("EMQX metrics: %w", err))
	} else {
		for _, m := range metrics {
			for _, key := range []string{"messages.received", "messages.sent", "messages.dropped", "messages.publish", "bytes.received", "bytes.sent", "packets.received", "packets.sent"} {
				if v := number(m[key]); v != nil {
					s.Metrics[key] += *v
				}
			}
		}
		s.MessagesReceived = metric(s.Metrics, "messages.received")
		s.MessagesSent = metric(s.Metrics, "messages.sent")
		s.MessagesDropped = metric(s.Metrics, "messages.dropped")
	}
	return s, errors.Join(errs...)
}

func metric(m map[string]float64, key string) *float64 {
	v, ok := m[key]
	if !ok {
		return nil
	}
	return &v
}
func number(raw json.RawMessage) *float64 {
	var n float64
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &n) != nil {
		return nil
	}
	return &n
}
func textValue(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if number(raw) != nil {
		return string(raw)
	}
	return ""
}

func (c *Client) get(ctx context.Context, endpoint string, dst any) error {
	client := http.Client{Timeout: 5 * time.Second}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
	}
	if client.Timeout <= 0 || client.Timeout > 5*time.Second {
		client.Timeout = 5 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid request")
	}
	req.SetBasicAuth(c.APIKey, c.APISecret)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("request failed (check connectivity and TLS trust)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	limit := c.MaxResponseBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	if limit > 8<<20 {
		limit = 8 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return errors.New("response read failed")
	}
	if int64(len(body)) > limit {
		return errors.New("response too large")
	}
	if json.Unmarshal(body, dst) != nil {
		return errors.New("invalid JSON response")
	}
	return nil
}

// Poll invokes receive synchronously; it must return promptly. API failures are
// observations and polling continues. Cancellation stops HTTP requests and timers.
func (c *Client) Poll(ctx context.Context, interval time.Duration, receive func(Snapshot, error)) error {
	if interval <= 0 || receive == nil {
		return errors.New("poll interval and receiver are required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, err := c.Fetch(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		receive(s, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
