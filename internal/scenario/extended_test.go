package scenario

import (
	"strings"
	"testing"
)

func TestMultipleWorkloadsAndVersionFive(t *testing.T) {
	s, err := Parse([]byte(`apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: groups
correlationRunId: 00112233445566778899aabbccddeeff
broker: {url: 'mqtt://localhost:1883', version: '5', connectTimeout: 1s, keepAlive: 10s, reconnect: {enabled: true, maxAttempts: 3, initialBackoff: 10ms, maxBackoff: 1s}}
clients: {count: 2, idTemplate: 'client-${sequence}'}
stages: [{duration: 1s, targetClients: 2}]
workloads:
 - {name: sub, type: subscriber, clients: 1, topic: test, qos: 1}
 - {name: pub, type: publisher, clients: 1, topic: test, qos: 1, ratePerClient: 10, payload: {type: static, value: hello}}
`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	round, err := Parse(data)
	if err != nil || round.Stages[0].Duration != s.Stages[0].Duration {
		t.Fatalf("round trip: %s %v", data, err)
	}
}

func TestRejectDangerousScenarioBounds(t *testing.T) {
	valid := `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: bounds
broker: {url: 'mqtt://localhost:1883', version: '3.1.1'}
clients: {count: 2, idTemplate: 'client-${sequence}'}
stages: [{duration: 1s, targetClients: 2}]
workloads: [{type: publisher, clients: 2, topic: test, qos: 0, ratePerClient: 1, payload: {type: static}}]
`
	for _, tt := range []struct{ from, to string }{{"ratePerClient: 1", "ratePerClient: .nan"}, {"topic: test", "topic: test/#"}, {"client-${sequence}", "fixed-id"}, {"client-${sequence}", "client-${sequence:999999999}"}, {"version: '3.1.1'", "version: '3.1.1', connectTimeout: -1s"}} {
		t.Run(tt.to, func(t *testing.T) {
			if _, err := Parse([]byte(strings.Replace(valid, tt.from, tt.to, 1))); err == nil {
				t.Fatal("invalid scenario accepted")
			}
		})
	}
	if _, err := Parse([]byte(valid + "---\nname: silently ignored\n")); err == nil {
		t.Fatal("multiple documents silently accepted")
	}
}
