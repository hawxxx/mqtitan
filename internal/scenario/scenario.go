package scenario

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mqtitan/mqtitan/internal/payload"
	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	d.Duration = v
	return nil
}

type Scenario struct {
	APIVersion          string            `yaml:"apiVersion"`
	Kind                string            `yaml:"kind"`
	Name                string            `yaml:"name"`
	Seed                uint64            `yaml:"seed,omitempty"`
	Broker              Broker            `yaml:"broker"`
	Clients             Clients           `yaml:"clients"`
	Stages              []Stage           `yaml:"stages"`
	Workloads           []Workload        `yaml:"workloads"`
	Thresholds          map[string]string `yaml:"thresholds,omitempty"`
	SharedCorrelationID string            `yaml:"correlationRunId,omitempty"`
	Safety              Safety            `yaml:"safety,omitempty"`
	Telemetry           *Telemetry        `yaml:"telemetry,omitempty"`
}

type Broker struct {
	URL              string         `yaml:"url"`
	Version          string         `yaml:"version"`
	Username         string         `yaml:"username,omitempty"`
	Password         string         `yaml:"password,omitempty"`
	ConnectTimeout   Duration       `yaml:"connectTimeout"`
	KeepAlive        Duration       `yaml:"keepAlive"`
	CleanStart       bool           `yaml:"cleanStart"`
	OperationTimeout Duration       `yaml:"operationTimeout,omitempty"`
	TLS              TLS            `yaml:"tls,omitempty"`
	Reconnect        Reconnect      `yaml:"reconnect,omitempty"`
	DNS              *DNS           `yaml:"dns,omitempty"`
	AddressLabels    []AddressLabel `yaml:"addressLabels,omitempty"`
	// IdleTimeoutHint is the load balancer idle timeout; Plan warns when
	// keepAlive would not keep an idle connection under it.
	IdleTimeoutHint Duration `yaml:"idleTimeoutHint,omitempty"`
}

// AddressLabel names a broker address range for the distribution report.
type AddressLabel struct {
	CIDR  string `yaml:"cidr"`
	Label string `yaml:"label"`
}

// DNS controls how the broker hostname is resolved.
type DNS struct {
	ResolveEachDial bool     `yaml:"resolveEachDial,omitempty"`
	Resolvers       []string `yaml:"resolvers,omitempty"` // IP:port literals
	Pick            string   `yaml:"pick,omitempty"`      // "", "first", "random"
	Family          string   `yaml:"family,omitempty"`    // "", "any", "ipv4", "ipv6"
}

type Reconnect struct {
	Enabled        bool     `yaml:"enabled,omitempty"`
	MaxAttempts    int      `yaml:"maxAttempts,omitempty"`
	InitialBackoff Duration `yaml:"initialBackoff,omitempty"`
	MaxBackoff     Duration `yaml:"maxBackoff,omitempty"`
}

type TLS struct {
	Profile            string `yaml:"profile,omitempty"`
	CAPEM              string `yaml:"caPem,omitempty"`
	CertPEM            string `yaml:"certPem,omitempty"`
	KeyPEM             string `yaml:"keyPem,omitempty"`
	CAFile             string `yaml:"caFile,omitempty"`
	CertFile           string `yaml:"certFile,omitempty"`
	KeyFile            string `yaml:"keyFile,omitempty"`
	ServerName         string `yaml:"serverName,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify,omitempty"`
	// HostnameVerification is "", "san", "cn-fallback" or "none".
	HostnameVerification string `yaml:"hostnameVerification,omitempty"`
}

type Clients struct {
	Count          int    `yaml:"count"`
	IDTemplate     string `yaml:"idTemplate"`
	SequenceOffset int    `yaml:"sequenceOffset,omitempty"`
	Worker         string `yaml:"worker,omitempty"`
	// Lifetime ends each session after a random duration in [min, max] and
	// reconnects, producing steady connection churn.
	Lifetime *Lifetime `yaml:"lifetime,omitempty"`
}
type Lifetime struct {
	Min Duration `yaml:"min"`
	Max Duration `yaml:"max"`
}
type Stage struct {
	Duration      Duration `yaml:"duration"`
	TargetClients int      `yaml:"targetClients"`
}
type Payload struct {
	Type  string `yaml:"type"`
	Value string `yaml:"value,omitempty"`
	Size  int    `yaml:"size,omitempty"`
}
type Workload struct {
	Name          string  `yaml:"name"`
	Type          string  `yaml:"type"`
	Clients       int     `yaml:"clients"`
	Topic         string  `yaml:"topic"`
	QoS           byte    `yaml:"qos"`
	RatePerClient float64 `yaml:"ratePerClient"`
	Payload       Payload `yaml:"payload"`
	Retain        bool    `yaml:"retain,omitempty"`
	Correlate     bool    `yaml:"correlate,omitempty"`
}

func Load(path string) (Scenario, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	return Parse(b)
}

func Encode(s Scenario) ([]byte, error) { return yaml.Marshal(s) }
func Parse(b []byte) (Scenario, error) {
	var s Scenario
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return s, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return s, err
		}
		return s, errors.New("scenario must contain exactly one YAML document")
	}
	return s, s.Validate()
}

func (s Scenario) Validate() error {
	var problems []string
	if s.APIVersion != "mqtitan.io/v1alpha1" {
		problems = append(problems, "apiVersion must be mqtitan.io/v1alpha1")
	}
	if s.Kind != "Scenario" {
		problems = append(problems, "kind must be Scenario")
	}
	if s.Name == "" {
		problems = append(problems, "name is required")
	}
	if s.SharedCorrelationID != "" {
		id, err := hex.DecodeString(s.SharedCorrelationID)
		if err != nil || len(id) != 16 {
			problems = append(problems, "correlationRunId must contain exactly 32 hexadecimal characters")
		}
	}
	r := s.Broker.Reconnect
	if r.MaxAttempts < 0 || r.MaxAttempts > 1000 || r.InitialBackoff.Duration < 0 || r.MaxBackoff.Duration < 0 || (r.InitialBackoff.Duration > 0 && r.MaxBackoff.Duration > 0 && r.MaxBackoff.Duration < r.InitialBackoff.Duration) {
		problems = append(problems, "reconnect requires 0-1000 attempts and nonnegative ordered backoffs")
	}
	u, err := url.Parse(s.Broker.URL)
	if err != nil || u.Host == "" || (u.Scheme != "mqtt" && u.Scheme != "mqtts" && u.Scheme != "ws" && u.Scheme != "wss") {
		problems = append(problems, "broker.url must use mqtt, mqtts, ws, or wss")
	}
	if s.Broker.Version != "3.1.1" && s.Broker.Version != "5" && s.Broker.Version != "5.0" {
		problems = append(problems, "broker.version must be 3.1.1 or 5")
	}
	if s.Broker.ConnectTimeout.Duration < 0 || s.Broker.OperationTimeout.Duration < 0 || s.Broker.KeepAlive.Duration < 0 || s.Broker.KeepAlive.Duration > 65535*time.Second {
		problems = append(problems, "broker timeouts must be nonnegative and keepAlive at most 65535s")
	}
	if (s.Broker.TLS.CertFile == "") != (s.Broker.TLS.KeyFile == "") {
		problems = append(problems, "TLS certFile and keyFile must be configured together")
	}
	tls := s.Broker.TLS
	if (tls.CertPEM == "") != (tls.KeyPEM == "") {
		problems = append(problems, "TLS certPem and keyPem must be configured together")
	}
	if len(tls.CAPEM) > 128<<10 || len(tls.CertPEM) > 128<<10 || len(tls.KeyPEM) > 128<<10 {
		problems = append(problems, "TLS PEM material must be at most 128 KiB per field")
	}
	hasPEM := tls.CAPEM != "" || tls.CertPEM != "" || tls.KeyPEM != ""
	hasFiles := tls.CAFile != "" || tls.CertFile != "" || tls.KeyFile != ""
	if (tls.Profile != "" && (hasPEM || hasFiles)) || (hasPEM && hasFiles) {
		problems = append(problems, "TLS profile, PEM material and file paths cannot be combined")
	}
	if (tls.Profile != "" || hasPEM || hasFiles || tls.ServerName != "" || tls.HostnameVerification != "") && u != nil && u.Scheme != "mqtts" && u.Scheme != "wss" {
		problems = append(problems, "TLS settings require mqtts or wss")
	}
	switch tls.HostnameVerification {
	case "", "san", "cn-fallback", "none":
	default:
		problems = append(problems, "broker.tls.hostnameVerification must be san, cn-fallback or none")
	}
	if tls.HostnameVerification != "" && tls.InsecureSkipVerify {
		problems = append(problems, "broker.tls.hostnameVerification cannot be combined with insecureSkipVerify")
	}
	if d := s.Broker.DNS; d != nil {
		if len(d.Resolvers) > 16 {
			problems = append(problems, "broker.dns.resolvers allows at most 16 entries")
		}
		for i, r := range d.Resolvers {
			if _, err := netip.ParseAddrPort(r); err != nil {
				problems = append(problems, fmt.Sprintf("broker.dns.resolvers[%d] must be an IP:port literal", i))
			}
		}
		switch d.Pick {
		case "", "first", "random":
		default:
			problems = append(problems, "broker.dns.pick must be first or random")
		}
		switch d.Family {
		case "", "any", "ipv4", "ipv6":
		default:
			problems = append(problems, "broker.dns.family must be any, ipv4 or ipv6")
		}
	}
	if len(s.Broker.AddressLabels) > 256 {
		problems = append(problems, "broker.addressLabels allows at most 256 entries")
	} else {
		seen := map[netip.Prefix]bool{}
		for i, l := range s.Broker.AddressLabels {
			p, err := ParseAddressPrefix(l.CIDR)
			if err != nil {
				problems = append(problems, fmt.Sprintf("broker.addressLabels[%d].cidr %v", i, err))
			} else {
				if seen[p] {
					problems = append(problems, fmt.Sprintf("broker.addressLabels[%d].cidr duplicates an earlier entry", i))
				}
				seen[p] = true
			}
			if l.Label == "" {
				problems = append(problems, fmt.Sprintf("broker.addressLabels[%d].label is required", i))
			} else if len(l.Label) > 64 {
				problems = append(problems, fmt.Sprintf("broker.addressLabels[%d].label must be at most 64 bytes", i))
			}
		}
	}
	if s.Broker.IdleTimeoutHint.Duration < 0 {
		problems = append(problems, "broker.idleTimeoutHint must be nonnegative")
	}
	if s.Clients.Count <= 0 {
		problems = append(problems, "clients.count must be positive")
	}
	if l := s.Clients.Lifetime; l != nil {
		if l.Min.Duration <= 0 {
			problems = append(problems, "clients.lifetime.min must be positive")
		} else if l.Max.Duration < l.Min.Duration {
			problems = append(problems, "clients.lifetime.max must not be less than min")
		}
	}
	if s.Clients.IDTemplate == "" {
		problems = append(problems, "clients.idTemplate is required")
	}
	if s.Clients.Count > 1 && !sequencePattern.MatchString(s.Clients.IDTemplate) {
		problems = append(problems, "clients.idTemplate requires ${sequence} for unique IDs")
	}
	if s.Clients.SequenceOffset < 0 {
		problems = append(problems, "clients.sequenceOffset cannot be negative")
	}
	for _, match := range sequencePattern.FindAllStringSubmatch(s.Clients.IDTemplate, -1) {
		if match[1] != "" {
			width, err := strconv.Atoi(match[1])
			if err != nil || width > 128 {
				problems = append(problems, "sequence padding width must be at most 128")
			}
		}
	}
	if len(s.Stages) == 0 {
		problems = append(problems, "at least one stage is required")
	}
	for i, st := range s.Stages {
		if st.Duration.Duration <= 0 {
			problems = append(problems, fmt.Sprintf("stages[%d].duration must be positive", i))
		}
		if st.TargetClients < 0 || st.TargetClients > s.Clients.Count {
			problems = append(problems, fmt.Sprintf("stages[%d].targetClients outside client population", i))
		}
	}
	if len(s.Workloads) == 0 {
		problems = append(problems, "at least one workload is required")
	}
	population := 0
	for _, w := range s.Workloads {
		population += w.Clients
		if w.Type != "publisher" && w.Type != "subscriber" && w.Type != "connection" && w.Type != "mixed" {
			problems = append(problems, "unknown workload type "+w.Type)
		}
		if w.Clients <= 0 || w.Clients > s.Clients.Count {
			problems = append(problems, "workload clients outside client population")
		}
		if w.Topic == "" && w.Type != "connection" {
			problems = append(problems, "workload topic is required")
		}
		if (w.Type == "publisher" || w.Type == "mixed") && strings.ContainsAny(w.Topic, "+#\x00") {
			problems = append(problems, "publisher topics cannot contain wildcard or NUL characters")
		}
		if w.QoS > 2 {
			problems = append(problems, "qos must be 0, 1, or 2")
		}
		if (w.Type == "publisher" || w.Type == "mixed") && (w.RatePerClient <= 0 || math.IsNaN(w.RatePerClient) || math.IsInf(w.RatePerClient, 0) || w.RatePerClient > 1e6) {
			problems = append(problems, "ratePerClient must be positive")
		}
		if w.Payload.Size < 0 || w.Payload.Size > 16<<20 {
			problems = append(problems, "payload size must be between zero and 16 MiB")
		}
		if w.Payload.Type != "" && w.Payload.Type != "static" && w.Payload.Type != "random" && w.Payload.Type != "json" {
			problems = append(problems, "unsupported payload type")
		}
		if w.Payload.Type == "json" && w.Payload.Value != "" && !json.Valid([]byte(templateVariable.ReplaceAllString(w.Payload.Value, "0"))) {
			problems = append(problems, "payload.value must be valid JSON")
		} else if err := checkPayload(w.Payload); err != nil {
			problems = append(problems, fmt.Sprintf("workload %q payload: %v", w.Name, err))
		}
	}
	if population > s.Clients.Count {
		problems = append(problems, "workload populations exceed clients.count")
	}
	problems = append(problems, s.Safety.check(s)...)
	if s.Telemetry != nil {
		problems = append(problems, s.Telemetry.check()...)
	} else if s.hasBalanceThreshold() {
		problems = append(problems, "thresholds broker_connection_cv and zone_connection_skew require a telemetry block")
	}
	if len(problems) == 0 && s.needsRenderCheck() {
		if err := s.CheckRendered(s.Clients.Worker, s.Clients.SequenceOffset, s.Clients.SequenceOffset+s.Clients.Count); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// checkPayload compiles the template the way the engine does, so unknown
// variables, oversize expansions and invalid expanded JSON fail validation
// instead of failing the run after it starts.
func checkPayload(p Payload) error {
	if p.Size < 0 || p.Size > 16<<20 {
		return nil // reported above
	}
	value := strings.ReplaceAll(Expand(p.Value, 1, "client-1"), "${worker}", "worker")
	_, err := payload.New(payload.Spec{Type: p.Type, Value: value, Size: p.Size}, payload.Variables{ClientID: "client-1", Worker: "worker", Sequence: 1})
	return err
}

type Plan struct {
	PeakClients                 int
	Duration                    time.Duration
	MessagesPerSecond           float64
	PayloadBytes                int
	EstimatedMemoryBytes        int64
	EstimatedFileDescriptors    int
	EstimatedWireBytesPerSecond float64
	// Warnings flag workloads that no stage ever reaches; they would silently
	// contribute nothing (for example subscribers listed after publishers).
	Warnings []string
	// ChurnConnectsPerSecond is the steady reconnect rate caused by
	// clients.lifetime at peak; zero without it.
	ChurnConnectsPerSecond float64
}

// PlanOptions refine Plan with facts only the caller knows.
type PlanOptions struct {
	Workers      int // zero means 1
	Destinations int // resolved broker addresses; zero derives from addressLabels, else 1
}

func (s Scenario) Plan() Plan { return s.PlanWith(PlanOptions{}) }

// hasBalanceThreshold reports whether a threshold judges broker telemetry.
func (s Scenario) hasBalanceThreshold() bool {
	_, cv := s.Thresholds["broker_connection_cv"]
	_, skew := s.Thresholds["zone_connection_skew"]
	return cv || skew
}

func (s Scenario) PlanWith(o PlanOptions) Plan {
	peak := 0
	var duration time.Duration
	for _, st := range s.Stages {
		if st.TargetClients > peak {
			peak = st.TargetClients
		}
		duration += st.Duration.Duration
	}
	payload := 0
	rate := 0.0
	offset := 0
	var warnings []string
	for _, w := range s.Workloads {
		n := max(0, min(w.Clients, peak-offset))
		if n == 0 {
			warnings = append(warnings, fmt.Sprintf("workload %q (clients %d-%d) never connects: the highest stage target is %d; reorder workloads or raise a stage target", w.Name, offset+1, offset+w.Clients, peak))
		}
		offset += w.Clients
		size := max(len(w.Payload.Value), w.Payload.Size)
		payload = max(payload, size)
		if w.Type == "publisher" || w.Type == "mixed" || w.Type == "" {
			rate += float64(n) * w.RatePerClient
		}
	}
	warnings = append(warnings, s.idleWarnings()...)
	if t := s.Telemetry; t != nil && t.Prometheus != nil && t.ExpectedTargets == 0 && s.hasBalanceThreshold() {
		warnings = append(warnings, "telemetry.expectedTargets is 0; a pod missing from Prometheus will not fail balance thresholds")
	}
	if w := s.portWarning(peak, o); w != "" {
		warnings = append(warnings, w)
	}
	churn := 0.0
	if l := s.Clients.Lifetime; l != nil {
		if mean := (l.Min.Duration + l.Max.Duration) / 2; mean > 0 {
			churn = float64(peak) / mean.Seconds()
		}
	}
	return Plan{
		PeakClients:                 peak,
		Duration:                    duration,
		MessagesPerSecond:           rate,
		PayloadBytes:                payload,
		EstimatedMemoryBytes:        int64(peak) * 96 * 1024,
		EstimatedFileDescriptors:    peak*2 + 128,
		EstimatedWireBytesPerSecond: rate * float64(payload+64),
		Warnings:                    warnings,
		ChurnConnectsPerSecond:      churn,
	}
}

// ParseAddressPrefix parses a broker.addressLabels CIDR into its masked form.
// IPv4-mapped IPv6 prefixes are rejected: their bit count is relative to the
// 128-bit address and cannot be unmapped without changing the range.
func ParseAddressPrefix(cidr string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Prefix{}, errors.New("is not a valid CIDR")
	}
	if p.Addr().Is4In6() {
		return netip.Prefix{}, fmt.Errorf("must be an IPv4 or IPv6 prefix, not IPv4-mapped (use %s)", "an IPv4 CIDR such as 198.51.100.0/24")
	}
	return p.Masked(), nil
}

// Telemetry configures controller-side broker telemetry polled during a test.
// Exactly one source (emqx or prometheus) must be set. Credentials come from
// controller environment variables, never from YAML.
type Telemetry struct {
	Interval        Duration          `yaml:"interval,omitempty"`        // default 15s, min 5s
	ExpectedTargets int               `yaml:"expectedTargets,omitempty"` // 0 = no check
	Baseline        string            `yaml:"baseline,omitempty"`        // "", "none", "first"
	EMQX            []EMQXTarget      `yaml:"emqx,omitempty"`
	Prometheus      *PrometheusSource `yaml:"prometheus,omitempty"`
}
type EMQXTarget struct {
	Name string `yaml:"name"`
	Zone string `yaml:"zone,omitempty"`
	URL  string `yaml:"url"`
}
type PrometheusSource struct {
	URL         string `yaml:"url"`
	Query       string `yaml:"query"`
	TargetLabel string `yaml:"targetLabel,omitempty"` // default "pod"
	ZoneLabel   string `yaml:"zoneLabel,omitempty"`   // default "zone"
}

const (
	DefaultTelemetryInterval = 15 * time.Second
	MinTelemetryInterval     = 5 * time.Second
	MaxTelemetryTargets      = 1024
	MaxExpectedTargets       = 4096
)

// PollInterval returns the configured interval or the default.
func (t Telemetry) PollInterval() time.Duration {
	if t.Interval.Duration <= 0 {
		return DefaultTelemetryInterval
	}
	return t.Interval.Duration
}

func (t Telemetry) check() []string {
	var problems []string
	if t.Interval.Duration != 0 && t.Interval.Duration < MinTelemetryInterval {
		problems = append(problems, "telemetry.interval must be at least 5s")
	}
	if t.ExpectedTargets < 0 || t.ExpectedTargets > MaxExpectedTargets {
		problems = append(problems, "telemetry.expectedTargets must be between 0 and 4096")
	}
	switch t.Baseline {
	case "", "none", "first":
	default:
		problems = append(problems, "telemetry.baseline must be none or first")
	}
	hasEMQX, hasProm := len(t.EMQX) > 0, t.Prometheus != nil
	if hasEMQX == hasProm {
		problems = append(problems, "telemetry must set exactly one of emqx or prometheus")
	}
	checkURL := func(field, raw string) {
		u, err := url.Parse(raw)
		switch {
		case raw == "":
			problems = append(problems, field+" is required")
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			problems = append(problems, field+" must be an http or https URL")
		case u.User != nil:
			problems = append(problems, field+": credentials must not be in YAML")
		}
	}
	if len(t.EMQX) > MaxTelemetryTargets {
		problems = append(problems, "telemetry.emqx allows at most 1024 targets")
	} else {
		seen := map[string]bool{}
		for i, e := range t.EMQX {
			if e.Name == "" {
				problems = append(problems, fmt.Sprintf("telemetry.emqx[%d].name is required", i))
			} else if seen[e.Name] {
				problems = append(problems, fmt.Sprintf("telemetry.emqx[%d].name duplicates an earlier entry", i))
			}
			seen[e.Name] = true
			checkURL(fmt.Sprintf("telemetry.emqx[%d].url", i), e.URL)
		}
	}
	if p := t.Prometheus; p != nil {
		checkURL("telemetry.prometheus.url", p.URL)
		if p.Query == "" {
			problems = append(problems, "telemetry.prometheus.query is required")
		}
	}
	return problems
}
