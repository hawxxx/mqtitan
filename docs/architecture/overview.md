# Architecture and scalability

This describes the implemented engineering preview, not a million-client qualification. [Diagrams](diagrams.md) show the component, lifecycle, worker, live-control, measurement and authentication flows. See the [roadmap](../roadmap.md) and [verification evidence](../verification.md).

```text
CLI / React dashboard
        │ REST commands + aggregate SSE telemetry
        ▼
Go controller ── SQLite records and samples
        │ HTTP JSON assignments and heartbeats
        ├── local engine ── MQTT broker endpoint
        └── distributed workers ── MQTT broker endpoint
```

## Components and execution

One Go binary provides CLI, controller, and worker commands. React 19/TypeScript uses Vite, TanStack Router/Query/Table/Virtual, and uPlot. The controller validates scenarios, partitions client sequences, collects rollups, evaluates thresholds, and persists results. Only one run may be active at a time. Optional EMQX telemetry is independent of MQTT execution.

Each client has a cancellable lifecycle goroutine, with additional MQTT-library connection work. This is not a custom socket event-loop implementation. Stage changes create or cancel clients. The default connection-attempt pacer is 500/sec per engine, not a cluster-wide connection-rate limit.

Publishers follow a fixed per-client schedule and await publish completion (one in-flight publish per client). When a publish outlasts its slot, the slots that came due meanwhile are skipped and counted as `publishSlotsMissed`: a non-zero value means the generator or the acknowledgement path could not sustain the requested rate, so treat the achieved rate, not the requested one, as the load. Publish latency is recorded with coordinated-omission correction: a publish that takes longer than its period also contributes the delays its skipped slots would have seen (in at most 256 histogram steps). The correction covers time spent inside the publish call; a generator that wakes late (CPU starvation, GC) raises `publishSlotsMissed` without adding latency samples. Min/avg/max and `publishLatencyCount` count only real publishes, and the Prometheus histogram's `_count` follows its corrected buckets while `_sum` covers only real publishes. A publish or subscribe that exceeds `operationTimeout` is recorded in the `timeout` error category and the client closes that connection itself; the closed session is not reported as a broker disconnect. Payload templates compile once per client and reuse private buffers where safe. Global rates, unlimited mode, Poisson arrivals, dedicated burst scheduling, and automatic fault injection are not implemented.

[Live controls](../live-controls.md) notify engines of the latest override. Targets stay within configured client capacity; overrides change stage load targets, not test duration. Browser disconnects do not cancel a separately running controller. Local `quick`/`run` commands do stop their owned server when they finish.

## Metrics and backpressure

Atomic counters and sharded, mutex-protected cumulative HDR histograms record metrics. Snapshots merge distributions outside the publish hot path; histograms do not rotate independently every second. Errors use bounded categories rather than per-message persistence. Individual client IDs and topic names are not uncontrolled Prometheus labels.

Workers send cumulative aggregates and histogram distributions over gzip-compressed HTTP JSON, normally once per second; a populated report is about 1.2 KB compressed (140 KB uncompressed). Controller and workers must run the same release: older controllers reject compressed, session-bearing worker requests, and older workers cannot heartbeat to a newer controller because they do not send the registration session. Protobuf is not the current transport. SSE subscribers have one-snapshot buffers: new telemetry replaces stale pending snapshots. At most 128 subscribers attach to a run. Consumers can skip intermediate visual updates; they reconcile through REST on reconnect. There is no event-ID replay. Metrics retrieval is bounded to 3,600 samples.

Publish-operation latency is not delivery latency: for QoS 0 it is the socket write, for QoS 1/2 the time until PUBACK/PUBCOMP. Correlated delivery latency uses sender wall-clock timestamps; synchronize clocks manually. Messages whose timestamp lies in the future or more than ten minutes back are counted as `correlationRejected` rather than recorded, so a non-zero value indicates clock skew between hosts. Workers report their clock offset to the controller, but automatic offset correction is not implemented.

## Distributed failure behavior

Workers register unique IDs, receive client partitions and a future wall-clock start time, and report heartbeats and control revisions. Registration provides discovery, not Kubernetes autoscaling. Workers need broker connectivity and assigned trust material.

The heartbeat lease expires after 15 seconds. Worker loss stops the run: the controller asks surviving workers to stop, waits up to ten seconds for their final reports, records the lost worker IDs in `lostWorkers`, and keeps the lost workers' last cumulative counters while zeroing their live gauges. Automatic replacement and partition reassignment are not implemented. Reports carry a per-assignment sequence so a delayed report cannot overwrite a newer one, and each registration receives a session that heartbeats must present. Distributed `peakConnected` is the highest aggregate the controller observed in its once-per-second samples, and never less than the largest single-worker peak; it is not the sum of per-worker peaks, which can occur at different times. Controller restart marks previously active stored tests interrupted rather than restoring MQTT sockets. [Rerunning](../reruns.md) creates a fresh test ID without replacing prior results.

## Persistence and security

SQLite is the implemented database; PostgreSQL is not supported. The `records` table stores encrypted scenario, test, and certificate values using AES-GCM. The `samples` table stores aggregated JSON without record encryption. No raw message history is persisted. Back up the database and adjacent `.key` together; independent controllers must not share one database.

Compaction retains the latest cumulative sample per bucket: 5 seconds after one hour, 30 seconds after one day, and one minute after seven days. Samples older than 90 days are removed when compaction runs. This is not averaging or peak-preserving aggregation, nor a standalone retention daemon. Metadata is not automatically deleted with samples.

Authentication supports local mode, bearer tokens, basic authentication, OIDC (authorization code with PKCE and encrypted session cookies, plus bearer JWT verification), and trusted reverse-proxy authentication; see [authentication](../authentication.md). Native LDAP and role-based authorization are not implemented. Remote HTTP endpoints require TLS at the ingress/reverse proxy. Persistent certificate profiles survive pod/container replacement when their database volume survives; see [certificates](../certificates.md).

Prometheus aggregates are exposed at `/metrics`. OpenTelemetry samples internal operations rather than tracing each message. A pprof endpoint is not currently implemented.

## Scalability limits to qualify

| Limit | Current response and remaining work |
| --- | --- |
| File descriptors and ephemeral ports | Read-only doctor checks; multiple source hosts may be necessary |
| Client goroutines, timers, and MQTT buffers | Benchmark memory/CPU at increasing populations; no million-client guarantee |
| Network and acknowledgement throughput | Compare achieved rates with generator resources and broker telemetry |
| Histogram/snapshot cost | Sharded recording and off-path merges; benchmark telemetry overhead |
| Worker JSON and controller aggregation | Local rollups; qualify controller capacity as worker count grows |
| SQLite writes and history growth | Compaction and bounded reads; single-controller deployment |
| Slow browsers | Latest-wins SSE and aggregate charts; reconcile after reconnect |
| Controller/worker failure | Cancellation and interrupted results, not transparent failover |

Capacity plans are uncalibrated estimates. Large example scenarios express targets, not measured capacity. Establish generator baselines, inspect container as well as host limits, and perform soak, failure, and security qualification before production capacity commitments.
