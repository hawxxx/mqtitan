# Scenario specification v1

Scenario YAML is validated before execution. Unknown fields are errors. Durations use Go duration syntax. Rates are positive messages per second per publisher and may be fractional. This reference describes the implemented schema, not every feature in the product roadmap.

```yaml
apiVersion: mqtitan.io/v1alpha1
kind: Scenario
name: production-telemetry
broker:
  url: mqtt://localhost:1883
  version: "3.1.1"
  connectTimeout: 10s
  keepAlive: 30s
  cleanStart: true
clients:
  count: 10000
  idTemplate: client-${sequence:06}
stages:
  - duration: 2m
    targetClients: 10000
workloads:
  - name: telemetry
    type: publisher
    clients: 10000
    topic: devices/${clientId}/telemetry
    qos: 1
    ratePerClient: 1
    payload:
      type: json
      size: 512
thresholds:
  connection_error_rate: "<0.01%"
  publish_latency_p95: "<50ms"
```

Workload types are publisher, subscriber, mixed and connection. Publisher and mixed workloads require clients, topic, qos, ratePerClient and payload. Subscribers require clients, topic and qos; connection workloads require clients only. Workload groups occupy contiguous, disjoint client populations; their total clients must not exceed clients.count. A mixed client subscribes to and publishes on its own resolved topic. Use separate publisher and subscriber groups for fan-out. Stage targets select a prefix of the global client population, so put the groups that must always connect first: `plan` warns when a workload starts beyond the highest stage target and would never connect.

Payload values are compiled during validation exactly as the engine compiles them, so unknown variables, templates that expand beyond `size`, and JSON templates that expand to invalid JSON are rejected before a run starts. Numeric variables such as `${counter}` and `${random.float:18:32}` may appear unquoted in JSON templates; `${timestamp}` and `${uuid}` produce strings and must be quoted.

Broker versions 3.1.1 and 5 (or 5.0) select the protocol transport. MQTT 5 support does not imply configurable MQTT 5 properties, topic aliases or shared-subscription orchestration. Topic and client-ID templates support ${clientId}, ${sequence:06} and ${worker}; choose unique client-ID prefixes for concurrent runs. Distributed partitions preserve the global sequence offset.

Stages describe active client targets over their durations, including downward targets. Connection attempts and publisher pacing are load-generator work; inspect achieved rates before drawing conclusions about broker capacity. Requested targets are not measured capacity guarantees.

TLS uses mqtts URLs and verified system trust roots by default. Optional broker.tls fields are caFile, certFile, keyFile, serverName and insecureSkipVerify. Paths refer to files on each executing worker, so mount trust roots and client certificates there. certFile and keyFile must be provided together. insecureSkipVerify is only for isolated diagnostics and disables peer verification.

`broker.tls.hostnameVerification` selects how the server name (`serverName`, else the URL host) is checked: `san` (the default when empty) uses Go's standard verification, which requires a DNS or IP SAN; `cn-fallback` verifies the full chain against the trust roots with server-auth key usage first, then matches the name against the SANs, or against every subject CN when the certificate has no DNS or IP SAN; `none` verifies the chain and key usage only and skips the name check. CN matching is case-insensitive and ignores a trailing dot; a wildcard must be a leading `*.` that covers exactly one label and needs at least two labels after it (`*.region-1.example.net` matches `a.region-1.example.net`, not `a.b.region-1.example.net`, `region-1.example.net` or `*.net`), partial wildcards such as `emqtt*.example.net` never match, and IP literals never match a CN. Any DNS or IP SAN disables the CN fallback. The field exists because Go rejects certificates that only carry a Common Name (`x509: certificate relies on legacy Common Name field, use SANs instead`); reissuing the broker certificate with a SAN is the preferred long-term fix. It requires mqtts or wss and cannot be combined with `insecureSkipVerify`.

`broker.dns` controls how the broker host is resolved. `resolvers` is a list of up to 16 `IP:port` literals queried in strict round-robin; a resolver that times out (2s per attempt) or answers SERVFAIL is skipped for the next one and a lookup that exhausts them fails as a `dns` connect failure, while NXDOMAIN fails immediately. An empty list uses the system resolver. The host is always queried as a rooted name, so `resolv.conf` `search`/`ndots` never add suffixes. `family` is `any` (default), `ipv4` or `ipv6`. `pick` is `first` (default, resolver order) or `random` (shuffled with the scenario `seed`); addresses are tried in order until one connects. By default the first answer set is cached for the run; `resolveEachDial: true` looks the host up again for every connection. TLS still verifies `serverName`, else the URL host, never the resolved IP. Without a `dns` block the system dialer is used unchanged.

`broker.addressLabels` is a list of up to 256 `{cidr, label}` entries (labels up to 64 bytes, CIDRs unique). Each connection's remote address is attributed to the label of the longest matching CIDR, or `unlabelled`; the result carries `addressLabels` next to the per-address `remoteAddresses`, `dnsAnswers` and `dnsErrors` counters (1024 keys each, then `other`). IPv4-mapped IPv6 addresses are treated as IPv4.

Broker username and password are optional runtime input. Do not commit credentials or encode them in broker.url. Unknown fields are rejected.

Connection churn is opt-in through clients.lifetime: each session ends after a random duration drawn uniformly from [min, max] (deterministic per seed, client and session), the client sends an MQTT DISCONNECT and immediately connects again, independent of broker.reconnect. A churned end is not a failure: it records no disconnect error, uses no retry budget and has no backoff. Counters: `churned` (sessions ended by lifetime) and `churnReconnects` (sessions started after one), exposed as `emqxload_churned_total` and `emqxload_churn_reconnects_total`; a reconnect after a real failure still counts as `reconnects`. `mqtitan plan` prints the expected churn connect rate at peak.

Broker reconnect is opt-in through broker.reconnect.enabled. Optional maxAttempts, initialBackoff and maxBackoff bound retries; defaults are three retries after the initial attempt, one-second initial delay and thirty-second maximum delay. A configured zero maxAttempts selects the default rather than unlimited retries. The retry budget applies per outage: once a reconnected session stays up for at least maxBackoff, a later connection loss starts again with the full budget and the initial delay, so long soak tests survive independent broker interruptions. A fresh client restores subscriptions and publishing after connection loss. Automatic fault injection, configurable jitter and persistent-session duplicate/recovery guarantees are not implemented. Use controlled broker/network failures when qualifying recovery and distinguish these from deliberate staged population changes.

With `correlate: true` on a publishing workload, generated payloads include a 40-byte correlation envelope carrying a run identifier, timestamp and sequence. Account for this additional overhead when estimating wire traffic. Correlation is opt-in; ordinary payloads do not receive the envelope. Do not treat subscriber callbacks carrying another run's payload as this run's delivery latency. Publisher topics must not contain MQTT wildcards; subscriber filters may use them.

Distributed engines share a coordinator-assigned correlation run identifier. Delivery latency across hosts uses the sender's wall clock; synchronize worker clocks before interpreting it. Negative or greater-than-ten-minute samples are excluded, rather than turned into latency measurements.

Examples cover connection-only, telemetry, chat fan-out, mixed gateways, QoS 1, QoS 2, TLS and MQTT 5. Large-population examples describe sizing targets and must be qualified on the intended workers and broker. See [operations](operations.md).

## Secure WebSocket endpoints

Use `wss://load.example.com:443/mqtt` for MQTT over an HTTPS load balancer. `https://` is not a broker transport scheme. The load balancer and HAProxy must forward WebSocket upgrades to the broker's MQTT WebSocket listener; forwarding to an ordinary HTTP endpoint or raw MQTT TCP listener does not work. TLS options also apply to WSS; see [certificates](certificates.md).

One scenario currently uses one broker URL. A DNS name managed in Route 53 can point to a load balancer distributing new connections across brokers. Long-lived connections remain on their selected backend; this does not guarantee equal load per broker. An explicit list of independent broker URLs is not implemented.

## Thresholds and latency interpretation

Supported thresholds are `connection_error_rate`, `message_error_rate` (alias for publish errors), `publish_error_rate`, `subscribe_error_rate`, `publish_latency_p50`, `publish_latency_p95`, `publish_latency_p99`, `connect_latency_p50`, `connect_latency_p95`, `connect_latency_p99`, and `end_to_end_latency_p50`, `end_to_end_latency_p95`, `end_to_end_latency_p99`. End-to-end thresholds require `correlate: true` on a publisher with matching subscribers; connect thresholds require at least one successful connection. Comparisons support `<`, `<=`, `>`, and `>=`. Use percentages for error rates and durations for latency. Missing measurements fail a configured threshold rather than producing a false pass.

Publish latency is not automatically delivery latency: QoS 0 does not acknowledge delivery, and QoS 1/2 measurements cover the publish operation's acknowledgement path. Use matching subscribers and correlation for end-to-end measurements.
