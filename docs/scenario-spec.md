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

Broker username and password are optional runtime input. Do not commit credentials or encode them in broker.url. Unknown fields are rejected.

## TLS hostname verification

`broker.tls.hostnameVerification` selects how the server name (`serverName`, else the URL host) is checked: `san` (the default when empty) uses Go's standard verification, which requires a DNS or IP SAN; `cn-fallback` verifies the full chain against the trust roots with server-auth key usage first, then matches the name against the SANs, or against every subject CN when the certificate has no DNS or IP SAN; `none` verifies the chain and key usage only and skips the name check. CN matching is case-insensitive and ignores a trailing dot; a wildcard must be a leading `*.` that covers exactly one label and needs at least two labels after it (`*.zone-a.example.net` matches `a.zone-a.example.net`, not `a.b.zone-a.example.net`, `zone-a.example.net` or `*.net`), partial wildcards such as `broker*.example.net` never match, and IP literals never match a CN. Any DNS or IP SAN disables the CN fallback. The field exists because Go rejects certificates that only carry a Common Name (`x509: certificate relies on legacy Common Name field, use SANs instead`); reissuing the broker certificate with a SAN is the preferred long-term fix. It requires mqtts or wss and cannot be combined with `insecureSkipVerify`. Validation errors: `broker.tls.hostnameVerification must be san, cn-fallback or none`, `broker.tls.hostnameVerification cannot be combined with insecureSkipVerify`, and, for a non-TLS scheme, `TLS settings require mqtts or wss`.

## Broker resolution and address labels

`broker.dns` controls how the broker host is resolved. `resolvers` is a list of up to 16 `IP:port` literals queried in strict round-robin; a resolver that times out (2s per attempt) or answers SERVFAIL is skipped for the next one and a lookup that exhausts them fails as a `dns` connect failure, while NXDOMAIN fails immediately. An empty list uses the system resolver. The host is always queried as a rooted name, so `resolv.conf` `search`/`ndots` never add suffixes. `family` is `any` (default), `ipv4` or `ipv6`. `pick` is `first` (default, resolver order) or `random` (shuffled with the scenario `seed`); addresses are tried in order until one connects. By default the first answer set is cached for the run; `resolveEachDial: true` looks the host up again for every connection. TLS still verifies `serverName`, else the URL host, never the resolved IP. Without a `dns` block the system dialer is used unchanged. Validation errors: `broker.dns.resolvers[i] must be an IP:port literal`, `broker.dns.resolvers allows at most 16 entries`, `broker.dns.pick must be first or random`, `broker.dns.family must be any, ipv4 or ipv6`. Defaults: `resolvers: []` (system resolver), `pick: first`, `family: any`, `resolveEachDial: false`.

Resolver behaviour to account for when measuring a split. A recursive resolver caches an answer for the record TTL, so a precise weighted-split measurement lists the hosted zone's authoritative name servers (as `IP:53`) rather than a public recursive resolver; the answers still rotate per query, but a caching resolver repeats one answer set until the TTL expires. `pick: first` follows the order of the answers after Go's RFC 6724 address sorting, not the raw resolver order, so it depends on the resolver and the worker's addresses; `random` is the recommended default for balance measurements. Many workers outside a cloud have no global IPv6, so run `family: ipv4` and `family: ipv6` as separate runs instead of `any`. With several resolvers a failed attempt moves to the next one; each failure increments `dnsErrors` for that resolver (a resolver key is its `IP:port`) and the lookup fails as a `dns` connect failure only when all of them fail. Go's resolver consults `/etc/hosts` first and may retry per the `resolv.conf` `attempts` setting before the lookup counts as failed; `mqtitan plan --resolve` (`ResolveBroker`) uses the pure-Go resolver with the same `resolvers` list, so its answers can differ from the system resolver.

`broker.addressLabels` is a list of up to 256 `{cidr, label}` entries (labels up to 64 bytes, CIDRs unique). Each connection's remote address is attributed to the label of the longest matching CIDR, or `unlabelled`; the result carries `addressLabels` next to the per-address `remoteAddresses`, `dnsAnswers` and `dnsErrors` counters (1024 keys each, then `other`). IPv4-mapped IPv6 addresses are treated as IPv4. Validation errors: `broker.addressLabels[i].cidr is not a valid CIDR`, `broker.addressLabels[i].cidr must be an IPv4 or IPv6 prefix, not IPv4-mapped ...`, `broker.addressLabels[i].cidr duplicates an earlier entry`, `broker.addressLabels[i].label is required`, `broker.addressLabels[i].label must be at most 64 bytes`, `broker.addressLabels allows at most 256 entries`.

## Lifetime churn

Connection churn is opt-in through clients.lifetime: each session ends after a random duration drawn uniformly from [min, max] (deterministic per seed, client and session), the client sends an MQTT DISCONNECT, waits a jittered pause drawn uniformly from [100ms, 1s] (deterministic per seed, client and session) and connects again with the same client ID, independent of broker.reconnect. The pause lets the broker finish closing the previous session; reconnecting at once can be refused (EMQX answers CONNACK 3, server unavailable). A churned end is not a failure: it records no disconnect error, uses no retry budget and has no backoff beyond that pause. Counters: `churned` (sessions ended by lifetime) and `churnReconnects` (sessions started after one), exposed as `emqxload_churned_total` and `emqxload_churn_reconnects_total`; a reconnect after a real failure still counts as `reconnects`. `mqtitan plan` prints the expected churn connect rate at peak. Churn reconnects are not paced by the connection rate; only the 100ms to 1s pause spaces them out. With `broker.reconnect.enabled: false`, a failed connect right after a churn ends that client, so keep reconnect enabled when churn runs against a broker that may refuse connections. Default: absent (no churn). Validation errors: `clients.lifetime.min must be positive`, `clients.lifetime.max must not be less than min`.

## Reconnect, correlation and examples

Broker reconnect is opt-in through broker.reconnect.enabled. Optional maxAttempts, initialBackoff and maxBackoff bound retries; defaults are three retries after the initial attempt, one-second initial delay and thirty-second maximum delay. A configured zero maxAttempts selects the default rather than unlimited retries. The retry budget applies per outage: once a reconnected session stays up for at least maxBackoff, a later connection loss starts again with the full budget and the initial delay, so long soak tests survive independent broker interruptions. A fresh client restores subscriptions and publishing after connection loss. Automatic fault injection, configurable jitter and persistent-session duplicate/recovery guarantees are not implemented. Use controlled broker/network failures when qualifying recovery and distinguish these from deliberate staged population changes.

With `correlate: true` on a publishing workload, generated payloads include a 40-byte correlation envelope carrying a run identifier, timestamp and sequence. Account for this additional overhead when estimating wire traffic. Correlation is opt-in; ordinary payloads do not receive the envelope. Do not treat subscriber callbacks carrying another run's payload as this run's delivery latency. Publisher topics must not contain MQTT wildcards; subscriber filters may use them.

Distributed engines share a coordinator-assigned correlation run identifier. Delivery latency across hosts uses the sender's wall clock; synchronize worker clocks before interpreting it. Negative or greater-than-ten-minute samples are excluded, rather than turned into latency measurements.

Examples cover connection-only, telemetry, chat fan-out, mixed gateways, QoS 1, QoS 2, TLS and MQTT 5. Large-population examples describe sizing targets and must be qualified on the intended workers and broker. See [operations](operations.md).

## Secure WebSocket endpoints

Use `wss://load.example.com:443/mqtt` for MQTT over an HTTPS load balancer. `https://` is not a broker transport scheme. The load balancer and HAProxy must forward WebSocket upgrades to the broker's MQTT WebSocket listener; forwarding to an ordinary HTTP endpoint or raw MQTT TCP listener does not work. TLS options also apply to WSS; see [certificates](certificates.md).

## Load balancer and DNS names

One scenario uses one broker URL. A DNS name can point to a load balancer that distributes new connections across brokers, but long-lived connections stay on their selected backend, so equal connection counts are not guaranteed. To measure the split, use `broker.dns` to control resolution and `broker.addressLabels` to group the addresses that answers resolve to, and `telemetry` to compare per-broker and per-zone counts. See [`examples/nlb-balance.yaml`](../examples/nlb-balance.yaml) and the [compose proof](../deploy/e2e/balance/run.sh). An explicit list of independent broker URLs is not implemented.

## Idle timeouts and source ports

`broker.idleTimeoutHint` (duration, default none) is the load balancer idle timeout. `plan` and `validate` warn when a workload sends nothing for at least the hint (connection, subscriber, or a publisher slower than one message per hint) and the effective `keepAlive` (30s when unset) is not below it. Default: none (no check). Validation error: `broker.idleTimeoutHint must be nonnegative`; the warning text starts `workload "<name>" sends nothing for at least <hint>: keepAlive ... >= broker.idleTimeoutHint ...`.

A TCP proxy or load balancer with an idle timeout closes connections that send nothing for longer than that timeout, and a connection-only workload sends nothing but the MQTT keepalive PINGREQ. Set `keepAlive` to at most half the idle timeout, or add a low-rate publisher on a dedicated test topic (see the commented `keepalive` workload in `examples/nlb-balance.yaml`), and set `idleTimeoutHint` so `plan` and `validate` warn when the two disagree. Drops by the idle timeout are not counted separately; they appear as `disconnect` errors.

Source ports: with client IP preservation each worker source IP has about 28,000 ephemeral ports (the Linux default range 32768 to 60999) per destination `IP:port`. `mqtitan plan --workers N --resolve` resolves the broker and warns when the clients per worker exceed 25,000 per destination address; add workers or broker addresses. See [operations](operations.md#source-ports-and-idle-timeouts).

## Safety

`safety` holds guardrails: `forbiddenTopicPrefixes` rejects publisher topics and subscriber filters (including wildcard and `$share/` filters, and topics rendered from `${clientId}`) that overlap a prefix; `clientIdDenyRegex` rejects any rendered client ID that matches; `strictSecrets` makes a TLS key file with permissions wider than 0600, or inside a git work tree, an error instead of a warning. Rendered checks run only when needed and render at most `clients.count` IDs. The prefixes are never hardcoded; use values that match your fleet, for example `forbiddenTopicPrefixes: ["devices/", "telemetry/", "ops/"]` and a `clientIdDenyRegex` that matches device identifiers such as `^[0-9a-fA-F]{12}$`. `strictSecrets` refuses keys on filesystems that report 0777 for every file (for example WSL `/mnt/c`); copy the key to a Linux filesystem. Kubernetes Secret volumes mount files 0644 by default, so set `defaultMode: 0400` (or 0600) on the volume or `strictSecrets` refuses the key. Warnings name the key by basename only. Validation errors: `safety.forbiddenTopicPrefixes[i] must be a literal topic prefix (wildcards are not allowed)`, `workload "<name>" topic ... matches forbidden prefix "<p>"`, `safety.clientIdDenyRegex is not a valid regular expression`, `client ID "<id>" matches safety.clientIdDenyRegex`, and for a refused key `refusing key file <basename>: ... (safety.strictSecrets)`.

## Telemetry and balance thresholds

`telemetry` polls per-broker connection counts during the test (see [per-test telemetry](emqx.md#per-test-telemetry)). `interval` defaults to 15s (minimum 5s), `baseline` is `none` (default) or `first`, and exactly one of `emqx` or `prometheus` is required. Set `expectedTargets` (0 to 4096, default 0 = no check) for migration scenarios: otherwise a pod missing from Prometheus is not detected and the remaining brokers look balanced. A sample with fewer targets than expected is incomplete and the balance thresholds then fail with `incomplete broker telemetry`. Balance statistics only see the retained samples (2880 per test, about 12 hours at 15s), so for a longer run the peak may be outside the window. Validation errors: `telemetry must set exactly one of emqx or prometheus`, `telemetry.interval must be at least 5s`, `telemetry.expectedTargets must be between 0 and 4096`, `telemetry.baseline must be none or first`, `telemetry.emqx allows at most 1024 targets`, `telemetry.emqx[i].name is required`, `telemetry.emqx[i].name duplicates an earlier entry`, `telemetry.emqx[i].url` / `telemetry.prometheus.url` `is required`, `must be an http or https URL` or `credentials must not be in YAML`, and `telemetry.prometheus.query is required`. `targetLabel` defaults to `pod` and `zoneLabel` to `zone`.

## Connect failure stages

`connectFailures` in the results classifies each failed connect once by stage (`dns`, `tcp`, `tls_handshake`, `tls_verify_chain`, `tls_verify_hostname`, `tls_alert`, `websocket_upgrade`, `mqtt_connack`, `timeout`, `other`); the stages sum to `connectErrors`, and `connackReasons` counts MQTT CONNACK refusal codes. Unset stages are omitted.

## Thresholds and latency interpretation

Supported thresholds are `connection_error_rate`, `message_error_rate` (alias for publish errors), `publish_error_rate`, `subscribe_error_rate`, `publish_latency_p50`, `publish_latency_p95`, `publish_latency_p99`, `connect_latency_p50`, `connect_latency_p95`, `connect_latency_p99`, and `end_to_end_latency_p50`, `end_to_end_latency_p95`, `end_to_end_latency_p99`, `broker_connection_cv` and `zone_connection_skew`. End-to-end thresholds require `correlate: true` on a publisher with matching subscribers; connect thresholds require at least one successful connection. Comparisons support `<`, `<=`, `>`, and `>=`. Use percentages for error rates and durations for latency. The balance thresholds (`broker_connection_cv`, `zone_connection_skew`) take percentages that may exceed 100 and use the peak non-baseline telemetry sample: CV is the population standard deviation over the mean of per-target connections, skew is (max-min)/mean of per-zone totals. They need `telemetry`, at least two targets (CV) or two zones with every target zoned (skew), connections greater than zero in total, and complete telemetry: a failed or incomplete sample at or after the peak makes the balance thresholds fail with `incomplete broker telemetry`. Missing measurements fail a configured threshold rather than producing a false pass.

Publish latency is not automatically delivery latency: QoS 0 does not acknowledge delivery, and QoS 1/2 measurements cover the publish operation's acknowledgement path. Use matching subscribers and correlation for end-to-end measurements.
