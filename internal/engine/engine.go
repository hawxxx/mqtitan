package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/metrics"
	transport "github.com/mqtitan/mqtitan/internal/mqtt"
	"github.com/mqtitan/mqtitan/internal/payload"
	"github.com/mqtitan/mqtitan/internal/scenario"
	mrand "math/rand/v2"
	"net/netip"
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
	var prefixes []netip.Prefix
	var labels []string
	for _, l := range e.Scenario.Broker.AddressLabels {
		p, err := scenario.ParseAddressPrefix(l.CIDR)
		if err != nil {
			return fmt.Errorf("broker.addressLabels: %w", err)
		}
		prefixes = append(prefixes, p)
		labels = append(labels, l.Label)
	}
	e.Metrics.SetAddressLabels(prefixes, labels)
	connector, err := transport.NewConnectorWithHooks(e.Scenario.Broker, transport.Hooks{OnResolve: e.Metrics.RecordDNSAnswers, OnResolveError: e.Metrics.RecordDNSError, Seed: e.Scenario.Seed, StrictSecrets: e.Scenario.Safety.StrictSecrets})
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
	initial := backoff
	retries := 0
	// Only a session after one that connected is a reconnect; retries of a
	// client that never connected are connection attempts.
	everConnected := false
	kind := sessionFirst
	lives := 0
	for {
		up, churned := e.runSession(ctx, seq, kind, lives, g)
		lives++
		everConnected = everConnected || up > 0
		if ctx.Err() != nil {
			return
		}
		if churned {
			// A planned end is neither a failure nor a retry: reconnect at once.
			kind = sessionChurn
			retries = 0
			backoff = initial
			continue
		}
		kind = sessionFirst
		if everConnected {
			kind = sessionReconnect
		}
		if !r.Enabled {
			return
		}
		// A session that stayed connected for at least maxBackoff ends an outage;
		// the next loss gets a fresh retry budget instead of inheriting old retries.
		if up >= maximum {
			retries = 0
			backoff = initial
		}
		if retries >= attempts {
			return
		}
		retries++
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

// lifetimeFor draws the n-th session lifetime of client seq uniformly from the
// configured [min, max]; it is deterministic for a given seed.
func (e *Engine) lifetimeFor(seq, n int) time.Duration {
	l := e.Scenario.Clients.Lifetime
	lo, hi := l.Min.Duration, l.Max.Duration
	if hi <= lo {
		return lo
	}
	r := mrand.New(mrand.NewPCG(e.Scenario.Seed, uint64(seq)<<32|uint64(n)))
	return lo + time.Duration(r.Int64N(int64(hi-lo)+1))
}
func (e *Engine) generator(seq int) (*payload.Generator, error) {
	w := e.workload(seq)
	globalSeq := seq + e.Scenario.Clients.SequenceOffset
	id := strings.ReplaceAll(scenario.Expand(e.Scenario.Clients.IDTemplate, globalSeq, ""), "${worker}", e.Scenario.Clients.Worker)
	value := strings.ReplaceAll(scenario.Expand(w.Payload.Value, globalSeq, id), "${worker}", e.Scenario.Clients.Worker)
	return payload.New(payload.Spec{Type: w.Payload.Type, Value: value, Size: w.Payload.Size}, payload.Variables{ClientID: id, Worker: e.Scenario.Clients.Worker, Sequence: globalSeq, Seed: e.Scenario.Seed})
}

type sessionKind uint8

const (
	sessionFirst     sessionKind = iota // first attempt, or a retry before any connect
	sessionReconnect                    // after a session that failed
	sessionChurn                        // after a session ended by its lifetime
)

// runSession returns how long the session stayed connected (zero when
// connecting failed) and whether the client's lifetime ended it.
func (e *Engine) runSession(ctx context.Context, seq int, kind sessionKind, life int, g *payload.Generator) (up time.Duration, churned bool) {
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
			} else {
				e.Metrics.CorrelationRejected.Add(1)
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
		f := transport.Classify(err)
		e.Metrics.RecordConnectFailure(f.Stage, f.Code)
		e.Metrics.RecordError("connect")
		return
	}
	connectedAt := time.Now()
	e.Metrics.RecordConnectionLatencyFor(seq, connectedAt.Sub(connectStarted))
	switch kind {
	case sessionReconnect:
		e.Metrics.Reconnects.Add(1)
	case sessionChurn:
		e.Metrics.ChurnReconnects.Add(1)
	}
	e.Metrics.ClientConnected()
	e.Metrics.RecordRemote(c.RemoteAddr())
	// A session ended by a recorded subscribe failure is not an unexpected disconnect.
	failed := false
	var lifetime <-chan time.Time
	if e.Scenario.Clients.Lifetime != nil {
		t := time.NewTimer(e.lifetimeFor(seq, life))
		defer t.Stop()
		lifetime = t.C
	}
	// expire ends the session cleanly when its lifetime fires.
	expire := func() {
		churned, failed = true, true
		e.Metrics.Churned.Add(1)
		c.Disconnect()
	}
	defer func() {
		up = time.Since(connectedAt)
		c.Close()
		e.Metrics.ReleaseRemote(c.RemoteAddr())
		e.Metrics.Connected.Add(-1)
		e.Metrics.Disconnects.Add(1)
		if ctx.Err() == nil && !failed {
			e.Metrics.RecordError("disconnect")
		}
	}()
	if w.Type == "subscriber" || w.Type == "mixed" {
		op, cancel := context.WithTimeout(ctx, operationTimeout(e.Scenario.Broker))
		err = c.Subscribe(op, topic, w.QoS)
		timedOut := errors.Is(op.Err(), context.DeadlineExceeded)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.Metrics.SubscribeErrors.Add(1)
			e.Metrics.RecordError(failureCategory("subscribe", timedOut))
			failed = true
			return
		}
		e.Metrics.Subscriptions.Add(1)
	}
	if w.Type != "publisher" && w.Type != "mixed" {
		select {
		case <-ctx.Done():
		case <-c.Done():
		case <-lifetime:
			expire()
		}
		return
	}
	// Publishing follows a fixed schedule rather than a ticker. A publish that
	// outlasts its slot skips the slots that came due meanwhile; they are
	// counted as missed (generator or acknowledgement saturation) and their
	// queueing delay is added to the latency histogram, so a stall cannot hide
	// behind the samples it prevented.
	period := ratePeriod(w.RatePerClient)
	timer := time.NewTimer(period)
	defer timer.Stop()
	next := time.Now().Add(period)
	load, changed := e.Control.Current()
	publish := timer.C
	applyRate := func() {
		if load.Revision == 0 {
			return
		}
		timer.Stop()
		if load.RatePerClient == 0 {
			publish = nil
			return
		}
		period = ratePeriod(load.RatePerClient)
		next = time.Now().Add(period)
		timer.Reset(period)
		publish = timer.C
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
		case <-lifetime:
			expire()
			return
		case <-changed:
			load, changed = e.Control.Current()
			applyRate()
			continue
		case <-publish:
		}
		scheduled := next
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
		timedOut := errors.Is(op.Err(), context.DeadlineExceeded)
		cancel()
		elapsed := time.Since(started)
		missed := uint64(max(0, time.Since(scheduled)/period))
		if missed > 0 {
			e.Metrics.PublishSlotsMissed.Add(missed)
		}
		next = scheduled.Add(time.Duration(missed+1) * period)
		timer.Reset(time.Until(next))
		if err != nil {
			if ctx.Err() != nil {
				e.Metrics.PublishCancelled.Add(1)
				return
			}
			e.Metrics.PublishErrors.Add(1)
			e.Metrics.RecordError(failureCategory("publish", timedOut))
			if timedOut {
				// The client closed its own connection after the timeout. End
				// the session now rather than publishing on the closed client;
				// its end is not an unexpected broker disconnect.
				failed = true
				return
			}
			continue
		}
		e.Metrics.Published.Add(1)
		e.Metrics.BytesSent.Add(uint64(len(data)))
		e.Metrics.RecordPublishLatencyCorrected(seq, elapsed, period)
	}
}

func ratePeriod(perSecond float64) time.Duration {
	return max(time.Microsecond, time.Duration(float64(time.Second)/perSecond))
}

// failureCategory separates operations that exceeded operationTimeout, and
// were abandoned locally, from errors the broker or transport reported.
func failureCategory(operation string, timedOut bool) string {
	if timedOut {
		return "timeout"
	}
	return operation
}
func operationTimeout(b scenario.Broker) time.Duration {
	if b.OperationTimeout.Duration > 0 {
		return b.OperationTimeout.Duration
	}
	return 10 * time.Second
}
func (e *Engine) String() string { return fmt.Sprintf("engine(%s)", e.Scenario.Name) }
