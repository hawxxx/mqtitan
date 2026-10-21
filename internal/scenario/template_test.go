package scenario

import (
	"strings"
	"testing"
)

func BenchmarkExpand(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = Expand("devices/${sequence:08}/${clientId}/telemetry", i, "client-00000042")
	}
}

func TestJSONPayloadAcceptsUnquotedNumericVariables(t *testing.T) {
	s := validScenario()
	s.Workloads[0].Payload = Payload{Type: "json", Value: `{"id":"${clientId}","temp":${random.float:18:32},"n":${counter}}`}
	if err := s.Validate(); err != nil {
		t.Fatalf("numeric template rejected: %v", err)
	}
	s.Workloads[0].Payload.Value = `{"temp":${counter}`
	if err := s.Validate(); err == nil {
		t.Fatal("malformed JSON template accepted")
	}
}

func validScenario() Scenario {
	return Scenario{APIVersion: "mqtitan.io/v1alpha1", Kind: "Scenario", Name: "json", Broker: Broker{URL: "mqtt://localhost:1883", Version: "5"}, Clients: Clients{Count: 1, IDTemplate: "c"}, Stages: []Stage{{Duration: Duration{1}, TargetClients: 1}}, Workloads: []Workload{{Type: "publisher", Clients: 1, Topic: "t", RatePerClient: 1}}}
}

func TestValidateCompilesPayloadTemplates(t *testing.T) {
	s := validScenario()
	s.Workloads[0].Payload = Payload{Type: "json", Value: `{"ts":${timestamp}}`}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("unquoted timestamp accepted: %v", err)
	}
	s.Workloads[0].Payload = Payload{Type: "static", Value: "${nope}"}
	if err := s.Validate(); err == nil {
		t.Fatal("unknown payload variable accepted")
	}
	s.Workloads[0].Payload = Payload{Type: "static", Value: "id=${sequence:06} w=${worker} ${counter}"}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
}
