# Architecture

## System shape

```text
CLI / React UI
      |
versioned controller API + SSE
      |
scenario compiler -- capacity planner -- scheduler -- result store
      |
bounded protobuf command/telemetry streams
      |
worker supervisor -- rate shards -- MQTT event loops -- local aggregation
      |                                      |
MQTT 3.1.1/5 brokers                 EMQX telemetry API
```

Controller owns durable test state, partitions immutable compiled plans, coordinates monotonic start deadlines, and persists compact rollups. Workers own MQTT sockets and load accuracy. UI consumes one-second aggregates. UI loss never affects a run.

## Boundaries

- `scenario`: YAML input, defaults, semantic validation, immutable execution plan.
- `engine`: client lifecycle, pacing, connection and publish execution, cancellation.
- `metrics`: lock-minimized counters, bounded histograms, one-second snapshots.
- `distributed`: worker registration, leases, partitioning, heartbeats, protobuf streams.
- `storage`: repository interfaces; SQLite default, PostgreSQL optional.
- `api`: REST control plane and resumable SSE telemetry.
- `emqx`: optional API adapter; never required by engine.

## Concurrency architecture

Workers partition clients into shards. Each shard owns client state and rate schedule. Initial implementation uses one goroutine per connected client because MQTT client libraries already allocate per-connection readers/writers. Production engine replaces this behind same interface with poller-based sockets after benchmarks show crossover point.

No hot path sends per-message telemetry. Atomic counters collect totals. Per-shard HDR histograms rotate each second and merge off hot path. Error keys use bounded reason-code categories plus fixed-size reservoir samples. Every queue has capacity and overload policy.

Load path priority:

1. MQTT socket work
2. lifecycle accuracy
3. local metric aggregation
4. controller telemetry
5. UI fidelity

Telemetry congestion drops intermediate snapshots and retains newest cumulative state. It never blocks publishers.

## Distributed correctness

- Controller assigns stable client index ranges; templates remain deterministic across worker counts.
- Workers acknowledge a plan before controller issues a future monotonic start deadline.
- Heartbeat lease expiry marks assigned capacity lost; controller does not silently reassign stateful MQTT sessions.
- Wall-clock offset is measured and reported. Durations use monotonic clocks.
- Commands carry test ID, plan digest, assignment generation, and idempotency key.
- Reconnected workers resume only matching active generation.

## Persistence model

Core entities:

- `scenarios`: versioned YAML plus normalized plan digest.
- `tests`: lifecycle, scenario version, verdict, start/end time.
- `worker_assignments`: index range, generation, health, lost capacity.
- `metric_buckets`: test, timestamp, resolution, dimensions, counters, histogram encoding.
- `error_aggregates`: category, reason code, count, bounded samples.
- `threshold_results`: expression, observed value, pass/fail.
- `secrets`: encrypted envelope only; references appear in scenarios.

One-second buckets stay during active/recent runs. Compaction writes 5-second, 30-second, then 1-minute buckets transactionally before deleting source buckets. Never store individual messages.

## Scalability risks and controls

- Goroutine and library overhead: benchmark memory/client; move transport to sharded pollers when sockets dominate.
- Global limiter contention: distribute permits into independent rate shards; reconcile drift once per second.
- Histogram contention: shard and rotate; merge snapshots outside hot path.
- TLS CPU: cache parsed trust roots, support session resumption, stagger handshakes, expose handshake saturation.
- File descriptors and ports: preflight limits, source-IP capacity, TIME_WAIT pressure, and socket budget.
- NIC saturation: estimate bidirectional wire bytes including MQTT/TLS overhead; report generator saturation.
- Controller bottleneck: workers send fixed-cardinality rollups, delta-compressed protobuf, bounded latest-wins queue.
- Browser pressure: SSE rollups at 1 Hz, uPlot typed arrays, TanStack Virtual, server-side dimension filtering.
- Storage growth: tiered downsampling and retention limits.
- Cardinality: controlled group/worker/QoS labels only. Client ID and raw topic never become labels.

## Security model

Local mode binds loopback without auth. Server mode requires configured auth. OIDC and reverse-proxy identity map to roles. Secrets use envelope encryption and never enter logs, metrics, URLs, or API responses. TLS verification defaults on. pprof defaults off and binds loopback when enabled.
