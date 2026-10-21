# Architecture diagrams

These diagrams describe implemented behavior. Where a capability is absent (automatic worker replacement, clock-offset correction), the diagrams show what happens instead. Prose details are in the [architecture overview](overview.md).

## Components

```mermaid
flowchart LR
  subgraph Clients
    UI[React dashboard]
    CLI[mqtitan CLI]
    Prom[Prometheus]
  end
  subgraph Controller["mqtitan serve (controller)"]
    API[REST API + SSE]
    Auth[Auth: token / basic / OIDC / trusted proxy]
    Mgr[Test manager]
    Reg[Worker registry]
    Eng[Local engine]
    DB[(SQLite: AES-GCM records + samples)]
  end
  subgraph Workers["mqtitan worker (optional)"]
    W1[Worker engine]
    W2[Worker engine]
  end
  Broker[(MQTT broker)]
  EMQX[EMQX management API]

  UI --> Auth
  CLI --> Auth
  Prom -->|/metrics| Auth
  Auth --> API --> Mgr
  Mgr --> DB
  Mgr --> Eng --> Broker
  Mgr <--> Reg
  W1 -->|register, gzip heartbeats| Reg
  W2 -->|register, gzip heartbeats| Reg
  W1 --> Broker
  W2 --> Broker
  API -.optional.-> EMQX
```

Only one test runs at a time. A test runs either on the local engine or on the selected workers.

## Test lifecycle

```mermaid
stateDiagram-v2
  [*] --> Validated: POST /tests (scenario parsed, thresholds valid)
  Validated --> Running: persisted, workers assigned
  Running --> Completed: stages finished, thresholds pass
  Running --> Failed: engine error, worker lost, threshold fails,\nno client connected, or every publish failed
  Running --> Stopped: POST /tests/{id}/stop
  Running --> Interrupted: controller restarted
  Completed --> [*]
  Failed --> [*]
  Stopped --> [*]
  Interrupted --> [*]
```

`Run again` creates a new test from the stored scenario; earlier results are never modified.

## Distributed run: registration to shutdown

```mermaid
sequenceDiagram
  autonumber
  participant W as Worker
  participant C as Controller
  participant B as Broker
  W->>C: POST /workers/register {id}
  C-->>W: 201 {session}
  loop every second
    W->>C: POST /workers/heartbeat {id, session, report{sequence, snapshot, HDR}}
    C-->>W: {assignment?, serverTime}
  end
  Note over C: POST /tests with workers: partition clients<br/>into contiguous ranges, startAt = now + 3s
  C-->>W: assignment {scenario partition, startAt}
  W->>W: wait until startAt (server time minus half RTT)
  W->>B: connect, subscribe, publish (own client range)
  W->>C: reports (cumulative counters, sequence + 1)
  alt all workers report done
    C->>C: merge snapshots and HDR distributions, evaluate thresholds
  else stop requested or a worker lease expires
    C-->>W: assignment.stop = true
    W->>B: disconnect
    W->>C: final report (done)
    C->>C: wait up to 10s for live workers, then finish
  end
  C->>C: release assignments
```

Worker assignments contain resolved broker credentials. The worker routes accept only `MQTITAN_WORKER_TOKEN` when it is configured, and every heartbeat must carry the session issued at registration.

## Worker lease and report handling

```mermaid
stateDiagram-v2
  [*] --> Ready: register (session issued)
  Ready --> Assigned: test starts on this worker
  Assigned --> Running: startAt reached
  Running --> Done: report.done
  Running --> Lost: no heartbeat for 15s
  Done --> Ready: test released
  Lost --> Ready: heartbeats resume, or any process registers after release
  Lost --> Pruned: idle and unseen for 150s
  Ready --> Pruned: unseen for 150s
  Pruned --> [*]

  state Running {
    [*] --> Accept
    Accept --> Ignore: sequence <= last (late delivery)
    Accept --> Reject422: counters backwards / bad HDR / unknown revision
    Ignore --> Accept
    Reject422 --> Accept: worker keeps load and registration
  }
```

A lost worker fails the run. Its last cumulative counters stay in the result, its live gauges are zeroed, and its ID is listed in `lostWorkers`. Partitions are not reassigned.

## Live load control

```mermaid
sequenceDiagram
  autonumber
  participant U as Dashboard
  participant C as Controller
  participant W as Worker / local engine
  U->>C: POST /tests/{id}/load {clients, ratePerClient, revision: n}
  alt revision != n, rate limit, or unhealthy worker
    C-->>U: 409 / 429 / 400
  else accepted
    C->>C: persist change (revision n+1) before applying
    C->>W: Control.Apply or assignment.load per partition
    C-->>U: test with pendingWorkers
    W->>C: report controlRevision = n+1
    Note over U,C: pendingWorkers clears once every worker acknowledges
  end
```

## Publishing and measurement

```mermaid
sequenceDiagram
  autonumber
  participant P as Publisher client
  participant B as Broker
  participant S as Subscriber client
  loop every 1/rate on a fixed schedule
    P->>B: PUBLISH (QoS 0 = socket write, QoS 1/2 = wait for PUBACK/PUBCOMP)
    B-->>P: PUBACK / PUBCOMP
    Note over P: publish latency = send to ack<br/>(HDR, coordinated-omission corrected)
    alt publish outlasted later slots
      Note over P: skipped slots -> publishSlotsMissed
    end
    alt exceeded operationTimeout
      Note over P: error "timeout", client closes its own connection
    end
    B->>S: PUBLISH (correlated payload: run ID + send timestamp)
    Note over S: end-to-end = receive wall clock - send timestamp<br/>negative or > 10 min -> correlationRejected
  end
```

End-to-end latency across hosts is only as accurate as clock synchronization between them; the controller records each worker's clock offset but does not correct for it.

## Telemetry aggregation and persistence

```mermaid
flowchart TB
  Rec[Per-client recording: atomic counters,<br/>32 sharded HDR histograms] --> Snap[Snapshot once per second]
  Snap -->|local run| Sample
  Snap -->|worker| Report[Report: counters + HDR distributions, gzip]
  Report --> Merge[Controller merge: sum counters,<br/>merge HDR, observed peak]
  Merge --> Sample[Sample: snapshot + message/byte rates]
  Sample --> SSE[SSE to dashboards, latest wins]
  Sample --> Store[(SQLite samples, compacted after 1h/1d/7d)]
  Sample --> Thr[Final thresholds on the last snapshot]
  Thr --> Result[(Encrypted test record)]
```

## Authentication and certificate handling

```mermaid
flowchart TB
  Req[HTTP request] --> WR{Worker route and<br/>MQTITAN_WORKER_TOKEN set?}
  WR -->|yes| WT{Bearer == worker token?}
  WT -->|no| R401[401]
  WT -->|yes| Sess{Heartbeat session matches?}
  Sess -->|no| R409[409 register again]
  Sess -->|yes| Assign[Assignment with resolved secrets]
  WR -->|no| WTok{Bearer == worker token?}
  WTok -->|yes| R403[403]
  WTok -->|no| A{Configured auth: token, basic,<br/>OIDC, or trusted proxy header}
  A -->|fail| R401
  A -->|pass| O{Mutating request with<br/>foreign Origin?}
  O -->|yes| R403
  O -->|no| H[Handler]
  H --> Scn{Scenario TLS}
  Scn -->|profile| Prof[Decrypt stored profile]
  Scn -->|inline PEM| PEM[Validate PEM size and key pair]
  Scn -->|caFile / certFile / keyFile| Rej[400: CLI-only]
```
