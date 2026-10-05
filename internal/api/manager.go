package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/engine"
	"github.com/mqtitan/mqtitan/internal/metrics"
	"github.com/mqtitan/mqtitan/internal/scenario"
	"github.com/mqtitan/mqtitan/internal/storage"
	"github.com/mqtitan/mqtitan/internal/thresholds"
	"go.opentelemetry.io/otel"
	"gopkg.in/yaml.v3"
	"log/slog"
	"strings"
	"sync"
	"time"
)

var ErrBusy = errors.New("a test is already running; stop it before starting another")

type Test struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Status      string              `json:"status"`
	StartedAt   time.Time           `json:"startedAt"`
	EndedAt     *time.Time          `json:"endedAt,omitempty"`
	Scenario    string              `json:"scenario"`
	Snapshot    metrics.Snapshot    `json:"snapshot"`
	Thresholds  []thresholds.Result `json:"thresholds"`
	Error       string              `json:"error,omitempty"`
	LoadControl *LoadControl        `json:"loadControl,omitempty"`
	LoadChanges []LoadChange        `json:"loadChanges,omitempty"`
}
type Sample struct {
	Timestamp   time.Time        `json:"timestamp"`
	Snapshot    metrics.Snapshot `json:"snapshot"`
	MessageRate float64          `json:"messageRate"`
	ByteRate    float64          `json:"byteRate"`
}
type SavedScenario struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	YAML      string    `json:"yaml"`
	CreatedAt time.Time `json:"createdAt"`
}
type Event struct {
	Kind string
	Data any
}
type execution struct {
	ctx         context.Context
	control     *engine.Control
	test        Test
	cancel      context.CancelFunc
	counters    *metrics.Counters
	subscribers map[chan Event]struct{}
	workerIDs   []string
	snapshot    func() metrics.Snapshot
}
type Manager struct {
	store   *storage.Store
	mu      sync.Mutex
	runs    map[string]*execution
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
	workers *workerRegistry
	broker  BrokerTelemetry
}

func NewManager(store *storage.Store) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{store: store, runs: make(map[string]*execution), ctx: ctx, cancel: cancel}
	records, err := store.List("tests", 1000)
	if err != nil {
		cancel()
		return nil, err
	}
	for _, r := range records {
		var t Test
		if err = json.Unmarshal(r, &t); err != nil {
			cancel()
			return nil, err
		}
		if t.Status == "running" {
			now := time.Now().UTC()
			t.Status = "interrupted"
			t.EndedAt = &now
			t.Error = "controller restarted before test completed"
			if err = m.persist(t); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	m.workers = newWorkerRegistry()
	return m, nil
}
func ID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b)
}
func decodeScenario(raw string) (scenario.Scenario, error) {
	s, err := scenario.Parse([]byte(raw))
	if err != nil {
		return s, err
	}
	return s, thresholds.Validate(s.Thresholds)
}
func (m *Manager) persist(t Test) error {
	_, span := otel.Tracer("mqtitan/controller").Start(m.ctx, "result.persist")
	defer span.End()
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return m.store.Put("tests", t.ID, b)
}

func (m *Manager) Start(raw string) (Test, error) { return m.StartOnWorkers(raw, nil) }
func (m *Manager) StartOnWorkers(raw string, workerIDs []string) (Test, error) {
	s, err := decodeScenario(raw)
	if err != nil {
		return Test{}, err
	}
	s, err = m.resolveCertificates(s)
	if err != nil {
		return Test{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Test{}, errors.New("controller is shutting down")
	}
	for _, r := range m.runs {
		if r.test.Status == "running" {
			return Test{}, ErrBusy
		}
	}
	ctx, cancel := context.WithCancel(m.ctx)
	t := Test{ID: ID(), Name: s.Name, Status: "running", StartedAt: time.Now().UTC(), Scenario: raw, Thresholds: []thresholds.Result{}, LoadControl: initialLoad(s)}
	counters := metrics.New()
	r := &execution{ctx: ctx, control: engine.NewControl(s.Clients.Count), test: t, cancel: cancel, counters: counters, subscribers: make(map[chan Event]struct{}), workerIDs: workerIDs, snapshot: counters.Snapshot}
	if len(workerIDs) > 0 {
		if err := m.assign(r, s, workerIDs); err != nil {
			cancel()
			return Test{}, err
		}
		r.snapshot = func() metrics.Snapshot { return m.workerSnapshot(r) }
	}
	if err = m.persist(t); err != nil {
		cancel()
		m.releaseAssignments(r)
		return Test{}, err
	}
	m.runs[t.ID] = r
	m.wg.Add(1)
	go m.execute(ctx, r, s)
	return publicTest(t), nil
}

func (m *Manager) execute(ctx context.Context, r *execution, s scenario.Scenario) {
	ctx, span := otel.Tracer("mqtitan/controller").Start(ctx, "scenario.execute")
	defer span.End()
	defer m.wg.Done()
	done := make(chan error, 1)
	e := engine.Engine{Scenario: s, Metrics: r.counters, Control: r.control}
	go func() {
		if len(r.workerIDs) > 0 {
			done <- m.runDistributed(ctx, r)
		} else {
			done <- e.Run(ctx)
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := r.snapshot()
	lastTime := time.Now()
	lastCompact := time.Now()
	record := func() {
		now := time.Now().UTC()
		snap := r.snapshot()
		elapsed := now.Sub(lastTime).Seconds()
		sample := Sample{now, snap, float64(snap.Published-last.Published) / elapsed, float64(snap.BytesSent-last.BytesSent) / elapsed}
		last = snap
		lastTime = now
		b, _ := json.Marshal(sample)
		if err := m.store.AddSample(r.test.ID, now.UnixNano(), b); err != nil {
			slog.Error("metric persistence failed", "test", r.test.ID)
		}
		m.mu.Lock()
		r.test.Snapshot = snap
		m.notify(r, Event{"metrics", sample})
		t := r.test
		if err := m.persist(t); err != nil {
			slog.Error("test persistence failed", "test", t.ID)
		}
		m.mu.Unlock()
		if now.Sub(lastCompact) >= time.Minute {
			if err := m.store.CompactSamples(r.test.ID, now); err != nil {
				slog.Warn("metric compaction failed", "test", t.ID)
			}
			lastCompact = now
		}
	}
	for {
		select {
		case <-ticker.C:
			record()
		case err := <-done:
			record()
			now := time.Now().UTC()
			results, thresholdErr := thresholds.Evaluate(s.Thresholds, r.snapshot())
			m.mu.Lock()
			r.test.EndedAt = &now
			r.test.Thresholds = results
			r.test.Status = "completed"
			if ctx.Err() != nil {
				r.test.Status = "stopped"
			} else if err != nil || thresholdErr != nil || !thresholds.Passed(results) || (r.test.Snapshot.ConnectAttempts > 0 && r.test.Snapshot.PeakConnected == 0) || (r.test.Snapshot.PublishErrors > 0 && r.test.Snapshot.Published == 0) {
				r.test.Status = "failed"
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				r.test.Error = "load engine failed; inspect aggregate errors"
			}
			if thresholdErr != nil {
				r.test.Error = thresholdErr.Error()
			}
			t := r.test
			m.notify(r, Event{"status", publicTest(t)})
			m.mu.Unlock()
			if err := m.persist(t); err != nil {
				slog.Error("final persistence failed", "test", t.ID)
			}
			m.releaseAssignments(r)
			m.mu.Lock()
			delete(m.runs, t.ID)
			m.mu.Unlock()
			return
		}
	}
}

func (m *Manager) notify(r *execution, e Event) {
	for ch := range r.subscribers {
		select {
		case ch <- e:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- e:
			default:
			}
		}
	}
}
func (m *Manager) Get(id string) (Test, error) {
	m.mu.Lock()
	if r := m.runs[id]; r != nil {
		t := m.withLoadStatus(r)
		m.mu.Unlock()
		return publicTest(t), nil
	}
	m.mu.Unlock()
	b, err := m.store.Get("tests", id)
	if err != nil {
		return Test{}, err
	}
	var t Test
	err = json.Unmarshal(b, &t)
	return publicTest(t), err
}
func (m *Manager) List() ([]Test, error) {
	records, err := m.store.List("tests", 1000)
	if err != nil {
		return nil, err
	}
	out := make([]Test, 0, len(records))
	for _, r := range records {
		var t Test
		if err = json.Unmarshal(r, &t); err != nil {
			return nil, err
		}
		current, err := m.Get(t.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, current)
	}
	return out, nil
}
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	r := m.runs[id]
	m.mu.Unlock()
	if r == nil {
		_, err := m.Get(id)
		return err
	}
	r.cancel()
	return nil
}
func (m *Manager) Subscribe(id string) (<-chan Event, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil {
		return nil, nil, storage.ErrNotFound
	}
	if len(r.subscribers) >= 128 {
		return nil, nil, errors.New("live stream subscriber limit reached")
	}
	ch := make(chan Event, 1)
	r.subscribers[ch] = struct{}{}
	ch <- Event{"status", publicTest(r.test)}
	return ch, func() { m.mu.Lock(); delete(r.subscribers, ch); m.mu.Unlock() }, nil
}
func (m *Manager) Samples(id string) ([]Sample, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	raw, err := m.store.Samples(id, 3600)
	if err != nil {
		return nil, err
	}
	out := make([]Sample, 0, len(raw))
	for _, r := range raw {
		var v Sample
		if err = json.Unmarshal(r, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	for i := 1; i < len(out); i++ {
		elapsed := out[i].Timestamp.Sub(out[i-1].Timestamp).Seconds()
		if elapsed > 0 {
			out[i].MessageRate = float64(out[i].Snapshot.Published-out[i-1].Snapshot.Published) / elapsed
			out[i].ByteRate = float64(out[i].Snapshot.BytesSent-out[i-1].Snapshot.BytesSent) / elapsed
		}
	}
	return out, nil
}
func (m *Manager) Close() { m.mu.Lock(); m.closed = true; m.cancel(); m.mu.Unlock(); m.wg.Wait() }
func (m *Manager) SaveScenario(name, raw string) (SavedScenario, error) {
	scenario, err := decodeScenario(raw)
	if err != nil {
		return SavedScenario{}, err
	}
	if _, err = m.resolveCertificates(scenario); err != nil {
		return SavedScenario{}, err
	}
	if name == "" || len(name) > 128 {
		return SavedScenario{}, errors.New("scenario name must contain 1 to 128 characters")
	}
	s := SavedScenario{ID(), name, raw, time.Now().UTC()}
	b, _ := json.Marshal(s)
	if err := m.store.Put("scenarios", s.ID, b); err != nil {
		return s, err
	}
	s.YAML = redact(s.YAML)
	return s, nil
}
func (m *Manager) Scenarios() ([]SavedScenario, error) {
	raw, err := m.store.List("scenarios", 1000)
	out := make([]SavedScenario, 0)
	if err != nil {
		return nil, err
	}
	for _, b := range raw {
		var s SavedScenario
		if err = json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		s.YAML = redact(s.YAML)
		out = append(out, s)
	}
	return out, nil
}
func publicTest(t Test) Test { t.Scenario = redact(t.Scenario); return t }
func redact(raw string) string {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		return "[redacted invalid scenario]"
	}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				key := strings.ToLower(n.Content[i].Value)
				if key == "password" || key == "secret" || key == "token" || key == "keypem" {
					n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "[REDACTED]"}
				} else {
					walk(n.Content[i+1])
				}
			}
		} else {
			for _, c := range n.Content {
				walk(c)
			}
		}
	}
	walk(&node)
	b, err := yaml.Marshal(&node)
	if err != nil {
		return "[redacted]"
	}
	return string(b)
}

func (m *Manager) RawScenario(id string) (string, error) {
	b, err := m.store.Get("scenarios", id)
	if err != nil {
		return "", err
	}
	var s SavedScenario
	if err = json.Unmarshal(b, &s); err != nil {
		return "", err
	}
	return s.YAML, nil
}
func (m *Manager) Description() string { return fmt.Sprintf("MQTTitan controller") }
