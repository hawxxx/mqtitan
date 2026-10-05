package scenario

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

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
}

type Broker struct {
	URL              string    `yaml:"url"`
	Version          string    `yaml:"version"`
	Username         string    `yaml:"username,omitempty"`
	Password         string    `yaml:"password,omitempty"`
	ConnectTimeout   Duration  `yaml:"connectTimeout"`
	KeepAlive        Duration  `yaml:"keepAlive"`
	CleanStart       bool      `yaml:"cleanStart"`
	OperationTimeout Duration  `yaml:"operationTimeout,omitempty"`
	TLS              TLS       `yaml:"tls,omitempty"`
	Reconnect        Reconnect `yaml:"reconnect,omitempty"`
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
}

type Clients struct {
	Count          int    `yaml:"count"`
	IDTemplate     string `yaml:"idTemplate"`
	SequenceOffset int    `yaml:"sequenceOffset,omitempty"`
	Worker         string `yaml:"worker,omitempty"`
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
	if (tls.Profile != "" || hasPEM || hasFiles || tls.ServerName != "") && u != nil && u.Scheme != "mqtts" && u.Scheme != "wss" {
		problems = append(problems, "TLS settings require mqtts or wss")
	}
	if s.Clients.Count <= 0 {
		problems = append(problems, "clients.count must be positive")
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
		if w.Payload.Type == "json" && w.Payload.Value != "" && !json.Valid([]byte(w.Payload.Value)) {
			problems = append(problems, "payload.value must be valid JSON")
		}
	}
	if population > s.Clients.Count {
		problems = append(problems, "workload populations exceed clients.count")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

type Plan struct {
	PeakClients                 int
	Duration                    time.Duration
	MessagesPerSecond           float64
	PayloadBytes                int
	EstimatedMemoryBytes        int64
	EstimatedFileDescriptors    int
	EstimatedWireBytesPerSecond float64
}

func (s Scenario) Plan() Plan {
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
	for _, w := range s.Workloads {
		n := max(0, min(w.Clients, peak-offset))
		offset += w.Clients
		size := max(len(w.Payload.Value), w.Payload.Size)
		payload = max(payload, size)
		if w.Type == "publisher" || w.Type == "mixed" || w.Type == "" {
			rate += float64(n) * w.RatePerClient
		}
	}
	return Plan{peak, duration, rate, payload, int64(peak) * 96 * 1024, peak*2 + 128, rate * float64(payload+64)}
}
