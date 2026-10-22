package scenario

import (
	"fmt"
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

func TestHostnameVerificationValidation(t *testing.T) {
	base := `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: hostname
broker: {url: '%URL%', version: '3.1.1', tls: {%TLS%}}
clients: {count: 2, idTemplate: 'client-${sequence}'}
stages: [{duration: 1s, targetClients: 2}]
workloads: [{type: publisher, clients: 2, topic: test, qos: 0, ratePerClient: 1, payload: {type: static}}]
`
	for _, tt := range []struct{ name, url, tls, want string }{
		{"cn-fallback mqtts", "mqtts://broker.example.net:8883", "hostnameVerification: cn-fallback", ""},
		{"none wss", "wss://broker.example.net/mqtt", "hostnameVerification: none", ""},
		{"san", "mqtts://broker.example.net:8883", "hostnameVerification: san", ""},
		{"strict", "mqtts://broker.example.net:8883", "hostnameVerification: strict", "broker.tls.hostnameVerification must be san, cn-fallback or none"},
		{"insecure combo", "mqtts://broker.example.net:8883", "hostnameVerification: cn-fallback, insecureSkipVerify: true", "cannot be combined with insecureSkipVerify"},
		{"plain scheme", "mqtt://broker.example.net:1883", "hostnameVerification: cn-fallback", "TLS settings require mqtts or wss"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := strings.NewReplacer("%URL%", tt.url, "%TLS%", tt.tls).Replace(base)
			_, err := Parse([]byte(doc))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDNSValidation(t *testing.T) {
	base := `apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: dns
broker: {url: 'mqtt://broker.example.net:1883', version: '3.1.1'%DNS%}
clients: {count: 2, idTemplate: 'client-${sequence}'}
stages: [{duration: 1s, targetClients: 2}]
workloads: [{type: publisher, clients: 2, topic: test, qos: 0, ratePerClient: 1, payload: {type: static}}]
`
	many := make([]string, 17)
	for i := range many {
		many[i] = fmt.Sprintf("'192.0.2.%d:53'", i+1)
	}
	for _, tt := range []struct{ name, dns, want string }{
		{"absent", "", ""},
		{"valid", ", dns: {resolvers: ['192.0.2.53:53', '[2001:db8::53]:53'], pick: random, family: ipv4, resolveEachDial: true}", ""},
		{"bad pick", ", dns: {pick: round}", "broker.dns.pick must be first or random"},
		{"bad family", ", dns: {family: ip4}", "broker.dns.family must be any, ipv4 or ipv6"},
		{"hostname resolver", ", dns: {resolvers: ['dns.google:53']}", "IP:port"},
		{"no port", ", dns: {resolvers: ['192.0.2.53']}", "IP:port"},
		{"too many", ", dns: {resolvers: [" + strings.Join(many, ",") + "]}", "at most 16"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(base, "%DNS%", tt.dns, 1)))
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
