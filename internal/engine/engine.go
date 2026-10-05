package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/metrics"
	transport "github.com/mqtitan/mqtitan/internal/mqtt"
	"github.com/mqtitan/mqtitan/internal/payload"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"strings"
	"time"
)

type Engine struct {
	Scenario       scenario.Scenario
	Metrics        *metrics.Counters
	ConnectionRate int
	Control        *Control
	runID          [16]byte
	connector      *transport.Connector
}
type worker struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (e *Engine) Run(ctx context.Context) error {
	if err := e.Scenario.Validate(); err != nil {
		return err
	}
	if e.Metrics == nil {
		e.Metrics = metrics.New()
	}
	if e.Scenario.SharedCorrelationID != "" {
		id, _ := hex.DecodeString(e.Scenario.SharedCorrelationID)
		copy(e.runID[:], id)
	} else if _, err := rand.Read(e.runID[:]); err != nil {
		return fmt.Errorf("run correlation ID: %w", err)
	}
	rate := e.ConnectionRate
	if rate <= 0 {
		rate = 500
	}
	connector, err := transport.NewConnector(e.Scenario.Broker)
	if err != nil {
		return err
	}
	e.connector = connector
	offset := 0
	for _, w := range e.Scenario.Workloads {
		offset += w.Clients
		if _, err := e.generator(min(offset, e.Scenario.Clients.Count)); err != nil {
			return fmt.Errorf("workload payload: %w", err)
		}
	}
	workers := map[int]*worker{}
	stopAbove := func(target int) {
		for i, w := range workers {
			if i > target {
				w.cancel()
			}
		}
		for i, w := range workers {
			if i > target {
				<-w.done
				delete(workers, i)
			}
		}
	}
	defer func() {
		for _, w := range workers {
			w.cancel()
		}
		for _, w := range workers {
			<-w.done
		}
		e.Metrics.TargetClients.Store(0)
	}()
	period := max(time.Microsecond, time.Second/time.Duration(rate))
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for _, stage := range e.Scenario.Stages {
		stageCtx, cancel := context.WithTimeout(ctx, stage.Duration.Duration)
	stageLoop:
		for {
			load, changed := e.Control.Current()
			target := stage.TargetClients
			if load.Revision > 0 {
				target = load.Clients
			}
			e.Metrics.TargetClients.Store(int64(target))
			if len(workers) > target {
				stopAbove(target)
			}
			var connect <-chan time.Time
			if len(workers) < target {
				connect = ticker.C
			}
			select {
			case <-stageCtx.Done():
				break stageLoop
			case <-changed:
				continue
			case <-connect:
			}
			if stageCtx.Err() != nil {
				break stageLoop
			}
			i := len(workers) + 1
			child, stopClient := context.WithCancel(ctx)
			w := &worker{cancel: stopClient, done: make(chan struct{})}
			workers[i] = w
			go func(seq int) { defer close(w.done); e.runClient(child, seq) }(i)
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
func (e *Engine) workload(seq int) scenario.Workload {
	offset := 0
	for _, w := range e.Scenario.Workloads {
		offset += w.Clients
		if seq <= offset {
			return w
		}
	}
	return scenario.Workload{Type: "connection"}
}
func (e *Engine) runClient(ctx context.Context, seq int) {
	g, err := e.generator(seq)
	if err != nil {
		e.Metrics.RecordError("other")
		return
	}
	r := e.Scenario.Broker.Reconnect
	attempts := r.MaxAttempts
	if attempts == 0 {
		attempts = 3
	}
	backoff := r.InitialBackoff.Duration
	if backoff <= 0 {
		backoff = time.Second
	}
	backoff = max(time.Millisecond, backoff)
	maximum := r.MaxBackoff.Duration
	if maximum <= 0 {
		maximum = 30 * time.Second
	}
	maximum = max(backoff, maximum)
	for attempt := 0; ; attempt++ {
		e.runSession(ctx, seq, attempt > 0, g)
		if ctx.Err() != nil || !r.Enabled || attempt >= attempts {
			return
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff > maximum/2 {
			backoff = maximum
		} else {
			backoff = min(maximum, backoff*2)
		}
	}
}
func (e *Engine) generator(seq int) (*payload.Generator, error) {
	w := e.workload(seq)
	globalSeq := seq + e.Scenario.Clients.SequenceOffset
	id := strings.ReplaceAll(scenario.Expand(e.Scenario.Clients.IDTemplate, globalSeq, ""), "${worker}", e.Scenario.Clients.Worker)
	value := strings.ReplaceAll(scenario.Expand(w.Payload.Value, globalSeq, id), "${worker}", e.Scenario.Clients.Worker)
	return payload.New(payload.Spec{Type: w.Payload.Type, Value: value, Size: w.Payload.Size}, payload.Variables{ClientID: id, Worker: e.Scenario.Clients.Worker, Sequence: globalSeq, Seed: e.Scenario.Seed})
}
func (e *Engine) runSession(ctx context.Context, seq int, reconnecting bool, g *payload.Generator) {
	w := e.workload(seq)
	globalSeq := seq + e.Scenario.Clients.SequenceOffset
	expand := func(template, clientID string) string {
		return strings.ReplaceAll(scenario.Expand(template, globalSeq, clientID), "${worker}", e.Scenario.Clients.Worker)
	}
	id := expand(e.Scenario.Clients.IDTemplate, "")
	topic := expand(w.Topic, id)
	receive := func(data []byte) {
		e.Metrics.Received.Add(1)
		e.Metrics.BytesReceived.Add(uint64(len(data)))
		if len(data) >= 40 && string(data[:8]) == "MQTITAN1" && bytes.Equal(data[8:24], e.runID[:]) {
			sent := int64(binary.BigEndian.Uint64(data[24:32]))
			if d := time.Since(time.Unix(0, sent)); d >= 0 && d <= 10*time.Minute {
				e.Metrics.RecordEndToEndLatencyFor(seq, d)
			}
		}
	}
	e.Metrics.ConnectAttempts.Add(1)
	e.Metrics.Connecting.Add(1)
	connectStarted := time.Now()
	c, err := e.connector.Connect(ctx, id, receive)
	e.Metrics.Connecting.Add(-1)
	if err != nil {
		if ctx.Err() != nil {
			e.Metrics.ConnectCancelled.Add(1)
			return
		}
		e.Metrics.ConnectErrors.Add(1)
		e.Metrics.RecordError("connect")
		return
	}
	e.Metrics.RecordConnectionLatencyFor(seq, time.Since(connectStarted))
	if reconnecting {
		e.Metrics.Reconnects.Add(1)
	}
	e.Metrics.ClientConnected()
	defer func() {
		c.Close()
		e.Metrics.Connected.Add(-1)
		e.Metrics.Disconnects.Add(1)
		if ctx.Err() == nil {
			e.Metrics.RecordError("disconnect")
		}
	}()
	if w.Type == "subscriber" || w.Type == "mixed" {
		op, cancel := context.WithTimeout(ctx, operationTimeout(e.Scenario.Broker))
		err = c.Subscribe(op, topic, w.QoS)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.Metrics.SubscribeErrors.Add(1)
			e.Metrics.RecordError("subscribe")
			return
		}
		e.Metrics.Subscriptions.Add(1)
	}
	if w.Type != "publisher" && w.Type != "mixed" {
		select {
		case <-ctx.Done():
		case <-c.Done():
		}
		return
	}
	period := max(time.Microsecond, time.Duration(float64(time.Second)/w.RatePerClient))
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	load, changed := e.Control.Current()
	publish := (<-chan time.Time)(ticker.C)
	applyRate := func() {
		if load.Revision == 0 {
			return
		}
		if load.RatePerClient == 0 {
			ticker.Stop()
			publish = nil
			return
		}
		ticker.Reset(max(time.Microsecond, time.Duration(float64(time.Second)/load.RatePerClient)))
		publish = ticker.C
	}
	applyRate()
	var correlated []byte
	if w.Correlate {
		correlated = make([]byte, 40+g.MaxSize())
		copy(correlated, "MQTITAN1")
		copy(correlated[8:24], e.runID[:])
	}
	var message uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
			return
		case <-changed:
			load, changed = e.Control.Current()
			applyRate()
			continue
		case <-publish:
		}
		body := g.Next()
		data := body
		if w.Correlate {
			data = correlated[:40+len(body)]
			binary.BigEndian.PutUint64(data[24:32], uint64(time.Now().UnixNano()))
			message++
			binary.BigEndian.PutUint64(data[32:40], message)
			copy(data[40:], body)
		}
		e.Metrics.PublishAttempts.Add(1)
		started := time.Now()
		op, cancel := context.WithTimeout(ctx, operationTimeout(e.Scenario.Broker))
		err = c.Publish(op, topic, w.QoS, w.Retain, data)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				e.Metrics.PublishCancelled.Add(1)
				return
			}
			e.Metrics.PublishErrors.Add(1)
			e.Metrics.RecordError("publish")
			if ctx.Err() != nil {
				return
			}
			continue
		}
		e.Metrics.Published.Add(1)
		e.Metrics.BytesSent.Add(uint64(len(data)))
		e.Metrics.RecordPublishLatencyFor(seq, time.Since(started))
	}
}
func operationTimeout(b scenario.Broker) time.Duration {
	if b.OperationTimeout.Duration > 0 {
		return b.OperationTimeout.Duration
	}
	return 10 * time.Second
}
func (e *Engine) String() string { return fmt.Sprintf("engine(%s)", e.Scenario.Name) }
