# Scenario examples

All examples use apiVersion mqtitan.io/v1alpha1 and kind Scenario. Validate before running. broker.url is the address from the executing worker's network namespace; use mqtt://broker:1883 inside Compose.

| File | Intended use |
| --- | --- |
| ci-smoke.yaml | 20 clients, short QoS 1 correctness smoke |
| 10k.yaml | 10,000 telemetry publishers |
| 100k.yaml | 100,000 telemetry publishers sizing target |
| 500k.yaml | 500,000 telemetry publishers sizing target |
| 1m.yaml | 1,000,000 telemetry publishers sizing target |
| qos1.yaml | QoS 1 acknowledgment workload |
| qos2.yaml | QoS 2 handshake workload |
| tls.yaml | Verified TLS using the system trust store |
| connection-storm.yaml | Connection-only upward and downward stages |
| chat.yaml | Ten publishers and ninety subscribers on one room |
| gateway.yaml | Clients that both publish and subscribe |
| mqtt5.yaml | MQTT 5 QoS 1 correctness workload |
| reconnect.yaml | Opt-in bounded reconnect with mixed clients |

Large files describe requested populations, not measured support. Do not execute them on a laptop or an unqualified production broker. Read ../docs/operations.md for port, memory, descriptor, NAT and generator limits. Change client ID prefixes between concurrent tests to avoid session takeover.

Reconnect is opt-in and bounded; introduce controlled broker/network failures separately to qualify recovery. Fault injection, configurable jitter and persistent-session duplicate guarantees are not implemented. See ../docs/scenario-spec.md for explicit limitations. Broker MQTT 5 support does not automatically mean every MQTT 5 property or reconnect model is implemented.
