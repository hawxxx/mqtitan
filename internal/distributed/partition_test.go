package distributed

import (
	"github.com/mqtitan/mqtitan/internal/scenario"
	"reflect"
	"testing"
	"time"
)

func TestPartitionsPreservePopulationAndStages(t *testing.T) {
	s := scenario.Scenario{Clients: scenario.Clients{Count: 11, IDTemplate: "c-${sequence}"}, Stages: []scenario.Stage{{Duration: scenario.Duration{Duration: time.Second}, TargetClients: 5}, {Duration: scenario.Duration{Duration: time.Second}, TargetClients: 11}}, Workloads: []scenario.Workload{{Name: "sub", Type: "subscriber", Clients: 3}, {Name: "pub", Type: "publisher", Clients: 8}}}
	wantCount := []int{4, 4, 3}
	wantFirst := []int{4, 1, 0}
	total := 0
	for i := 0; i < 3; i++ {
		p, err := Partition(s, i, 3)
		if err != nil {
			t.Fatal(err)
		}
		if p.Clients.Count != wantCount[i] || p.Stages[0].TargetClients != wantFirst[i] {
			t.Fatalf("partition %d: %+v", i, p)
		}
		for _, w := range p.Workloads {
			total += w.Clients
		}
	}
	if total != 11 {
		t.Fatalf("population duplicated/lost: %d", total)
	}
}

func TestPartitionEncodeParseKeepsBalanceFields(t *testing.T) {
	src := `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: round-trip
broker:
  url: mqtts://broker.example.net:8883
  version: "3.1.1"
  keepAlive: 30s
  idleTimeoutHint: 100s
  tls:
    caPem: placeholder
    serverName: broker-01.zone-a.example.net
    hostnameVerification: cn-fallback
  dns:
    resolveEachDial: true
    resolvers: ["192.0.2.53:53", "198.51.100.53:53"]
    pick: random
    family: ipv4
  addressLabels:
    - cidr: 192.0.2.0/24
      label: legacy-nlb
clients:
  count: 10
  idTemplate: mqtitan-${sequence:08}
  lifetime:
    min: 5m
    max: 15m
stages:
  - duration: 1m
    targetClients: 10
workloads:
  - name: conn
    type: connection
    clients: 10
safety:
  forbiddenTopicPrefixes: ["devices/"]
  clientIdDenyRegex: '^[0-9a-fA-F]{12}$'
  strictSecrets: true
telemetry:
  interval: 15s
  expectedTargets: 3
  baseline: first
  prometheus:
    url: http://prometheus.example.net:9090
    query: sum by (pod, zone) (emqx_live_connections_count)
thresholds:
  broker_connection_cv: "<10%"
`
	s, err := scenario.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		p, err := Partition(s, i, 3)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := scenario.Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := scenario.Parse(raw)
		if err != nil {
			t.Fatalf("partition %d: %v\n%s", i, err, raw)
		}
		for name, ok := range map[string]bool{
			"lifetime":             reflect.DeepEqual(got.Clients.Lifetime, s.Clients.Lifetime) && got.Clients.Lifetime != nil,
			"dns":                  reflect.DeepEqual(got.Broker.DNS, s.Broker.DNS) && got.Broker.DNS != nil,
			"addressLabels":        reflect.DeepEqual(got.Broker.AddressLabels, s.Broker.AddressLabels) && len(got.Broker.AddressLabels) == 1,
			"safety":               reflect.DeepEqual(got.Safety, s.Safety) && got.Safety.StrictSecrets,
			"idleTimeoutHint":      got.Broker.IdleTimeoutHint == s.Broker.IdleTimeoutHint && got.Broker.IdleTimeoutHint.Duration == 100*time.Second,
			"telemetry":            reflect.DeepEqual(got.Telemetry, s.Telemetry) && got.Telemetry != nil,
			"hostnameVerification": got.Broker.TLS.HostnameVerification == "cn-fallback",
		} {
			if !ok {
				t.Errorf("partition %d lost %s:\n%s", i, name, raw)
			}
		}
	}
}
