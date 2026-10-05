package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/api"
	"github.com/mqtitan/mqtitan/internal/engine"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

type Options struct {
	Controller, ID, Token string
	OnReady               func(bool)
}
type httpStatusError struct{ status int }

func (e httpStatusError) Error() string { return fmt.Sprintf("controller returned HTTP %d", e.status) }

type running struct {
	control    *engine.Control
	assignment api.Assignment
	counters   *metrics.Counters
	cancel     context.CancelFunc
	done       chan error
	finished   bool
	failure    string
}

func Run(ctx context.Context, opts Options) error {
	ready := func(v bool) {
		if opts.OnReady != nil {
			opts.OnReady(v)
		}
	}
	defer ready(false)
	u, err := url.Parse(opts.Controller)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("controller must be HTTP or HTTPS URL")
	}
	if opts.ID == "" {
		return errors.New("worker id required")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	base := strings.TrimRight(opts.Controller, "/")
	post := func(path string, body, out any) error {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r, err := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(b))
		if err != nil {
			return err
		}
		r.Header.Set("Content-Type", "application/json")
		if opts.Token != "" {
			r.Header.Set("Authorization", "Bearer "+opts.Token)
		}
		resp, err := client.Do(r)
		if err != nil {
			return errors.New("controller unreachable")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return httpStatusError{resp.StatusCode}
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil {
			return err
		}
		if out != nil {
			return json.Unmarshal(envelope.Data, out)
		}
		return nil
	}
	registered := false
	lastContact := time.Now()
	var current *running
	lastAssignment := ""
	defer func() {
		if current != nil {
			current.cancel()
			if !current.finished {
				<-current.done
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !registered {
			err = post("/api/v1/workers/register", api.Worker{ID: opts.ID, CPUCount: runtime.NumCPU(), SupportsLiveControl: true}, nil)
			if err == nil {
				registered = true
			} else {
				slog.Warn("worker registration failed", "worker", opts.ID)
			}
		}
		if registered {
			var memory runtime.MemStats
			runtime.ReadMemStats(&memory)
			h := api.Heartbeat{ID: opts.ID, MemoryBytes: memory.Sys, ClientTime: time.Now().UTC()}
			if current != nil {
				if !current.finished {
					select {
					case err := <-current.done:
						current.finished = true
						if err != nil && !errors.Is(err, context.Canceled) {
							current.failure = "load engine failed"
						}
					default:
					}
				}
				h.Report = &api.Report{AssignmentID: current.assignment.ID, Snapshot: current.counters.Snapshot(), Distribution: current.counters.ExportDistribution(), Done: current.finished, Error: current.failure}
				load, _ := current.control.Current()
				h.Report.ControlRevision = load.Revision
			}
			var response api.HeartbeatResponse
			before := time.Now()
			err = post("/api/v1/workers/heartbeat", h, &response)
			after := time.Now()
			if err == nil {
				lastContact = after
				ready(true)
				if response.Assignment != nil {
					a := *response.Assignment
					if a.Stop && current != nil && current.assignment.ID == a.ID {
						current.cancel()
					}
					if !a.Stop && a.ID != lastAssignment {
						if current != nil && !current.finished {
							current.cancel()
							<-current.done
						}
						s, parseErr := scenario.Parse([]byte(a.Scenario))
						runCtx, cancel := context.WithCancel(ctx)
						counters := metrics.New()
						current = &running{assignment: a, counters: counters, cancel: cancel, done: make(chan error, 1), control: engine.NewControl(s.Clients.Count)}
						lastAssignment = a.ID
						if parseErr != nil {
							current.finished = true
							current.failure = "invalid scenario assignment"
							cancel()
						} else {
							delay := a.StartAt.Sub(response.ServerTime) - after.Sub(before)/2
							run := current
							go func() {
								if delay < -time.Second {
									run.done <- errors.New("missed coordinated start")
									return
								}
								timer := time.NewTimer(max(0, delay))
								defer timer.Stop()
								select {
								case <-runCtx.Done():
									run.done <- runCtx.Err()
									return
								case <-timer.C:
								}
								e := engine.Engine{Scenario: s, Metrics: counters, Control: run.control}
								run.done <- e.Run(runCtx)
							}()
						}
					}
					if !a.Stop && current != nil && current.assignment.ID == a.ID && !current.finished && a.Load != nil {
						if applyErr := current.control.Apply(*a.Load); applyErr != nil {
							current.failure = "invalid live load assignment"
							current.cancel()
						}
					}
				} else if current != nil {
					current.cancel()
				}
			} else {
				ready(false)
				slog.Warn("worker heartbeat failed", "worker", opts.ID)
				var status httpStatusError
				if errors.As(err, &status) && status.status == 409 {
					registered = false
					if current != nil {
						current.cancel()
					}
				}
				if time.Since(lastContact) > 15*time.Second && current != nil {
					current.cancel()
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
