# REST API

The controller serves `/api/v1`. Success responses use `{ "data": ... }`; errors use `{ "error": { "code": ..., "message": ... } }`. CSV and SSE are exceptions. This is a preview interface.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz`, `/readyz` | Process probes, not broker capacity certification |
| GET | `/metrics` | Prometheus aggregates, including `emqxload_connect_failures_total{stage}` and `emqxload_connack_refusals_total{code}`, `emqxload_churned_total` and `emqxload_churn_reconnects_total`, and for the latest test with telemetry `emqxload_broker_connections{target,zone}`, `emqxload_broker_connection_cv`, `emqxload_zone_connection_skew_ratio` and `emqxload_broker_telemetry_complete`. Tests carry `balance` (peak sample statistics; ratios are `null` when infinite) |
| POST / GET | `/api/v1/tests` | Start / list tests |
| GET | `/api/v1/tests/{id}` | Status and metadata |
| POST | `/api/v1/tests/{id}/stop` | Request cancellation |
| POST | `/api/v1/tests/{id}/rerun` | Fresh run using original server-side scenario |
| POST | `/api/v1/tests/{id}/load` | Apply [live controls](live-controls.md) |
| GET | `/api/v1/tests/{id}/metrics` | Aggregate history |
| GET | `/api/v1/tests/{id}/stream` | Live SSE |
| GET | `/api/v1/tests/{id}/brokers` | Per-test broker telemetry samples (see [EMQX integration](emqx.md#per-test-telemetry)) |
| GET | `/api/v1/tests/{id}/export?format=json` | JSON report; `format=csv` also supported (columns: timestamp, connected, published, messages_per_second, bytes_per_second, p50_ms, p95_ms, p99_ms, connection_errors, publish_errors, received, end_to_end_p95_ms, subscribe_errors, connect_failures) |
| POST / GET | `/api/v1/scenarios` | Save / list scenarios |
| POST | `/api/v1/plan` | Validate and estimate a scenario |
| GET | `/api/v1/doctor` | Controller-host diagnostics |
| GET | `/api/v1/workers` | Registered workers |
| POST | `/api/v1/workers/register`, `/api/v1/workers/heartbeat` | Worker-agent protocol |
| POST / GET | `/api/v1/certificates` | Upload / list profiles |
| GET | `/api/v1/brokers/emqx` | Optional broker telemetry |

## Submit and repeat tests

This requires `curl`, `jq`, a running controller, and a reachable broker. It generates real traffic:

```sh
jq -n --rawfile scenario examples/10k.yaml '{scenario: $scenario}' |
  curl --fail-with-body -sS http://127.0.0.1:8080/api/v1/tests \
    -H 'Content-Type: application/json' --data-binary @-
```

The request accepts `scenario` (YAML text) and optional `workers` (worker IDs). Omit workers for local execution. A supplied `scenarioId` selects a saved scenario instead of inline YAML. Save a scenario with `{ "name": "telemetry", "yaml": "YAML text" }`. Planning accepts `{ "scenario": "YAML text", "workers": 1 }`; `workers` is optional (1 to 256, default 1) and sets the worker count used for the source-port warning. The response includes `Warnings` (workloads that never connect, `broker.idleTimeoutHint` and source-port warnings).

Creation returns 201; busy conflicts return 409, invalid requests normally 400, and missing tests 404. Stop returns 202 and cannot restart a finished test. Rerun needs no scenario or secret payload:

```sh
curl --fail-with-body -sS -X POST \
  http://127.0.0.1:8080/api/v1/tests/TEST_ID/rerun
curl --fail-with-body -sS \
  'http://127.0.0.1:8080/api/v1/tests/TEST_ID/export?format=csv'
curl -N http://127.0.0.1:8080/api/v1/tests/TEST_ID/stream
```

SSE types are `status`, `metrics`, `broker` and `heartbeat`. A `broker` event carries one telemetry sample (`timestamp`, `source`, `targets[]` with `target`, `zone`, `connections`, `raw` and optional `error`, `complete`, `baseline`, optional `error`) and is sent only for scenarios with a `telemetry` block. Slow consumers can skip snapshots; there is no `Last-Event-ID` replay. Fetch state/history after reconnecting. Lists return at most 1,000 records and metrics retrieval at most 3,600 samples. JSON export contains `data.test` and `data.samples`. Go duration fields serialize as nanoseconds unless a dedicated display field is used.

## Connect failure breakdown

Every snapshot (REST, SSE and JSON export) carries two additive maps that omit zero entries:

- `connectFailures` maps a stage to its count: `dns`, `tcp`, `tls_handshake`, `tls_verify_chain`, `tls_verify_hostname`, `tls_alert`, `websocket_upgrade`, `mqtt_connack`, `timeout`, `other`. The counts always sum to `connectErrors`. Timeouts take precedence over every stage except `dns`: a resolver that timed out is a `dns` failure.
- `connackReasons` maps a decimal CONNACK code (MQTT 3.1.1 return code or MQTT 5 reason code) to its count, for `mqtt_connack` failures.
- `remoteAddresses`, `addressLabels`, `dnsAnswers` and `dnsErrors` are lists of `{key, label?, connects, active}` rows, sorted by key and bounded to 1024 keys plus an `other` row. `remoteAddresses` keys are `IP:port` of the broker address connected to (`active` is the live connection count); `addressLabels` aggregates them by `broker.addressLabels` (longest CIDR wins, `unlabelled` for no match); `dnsAnswers` keys are resolved IPs (`connects` = times returned); `dnsErrors` keys are resolver `IP:port` (`connects` = failed attempts). `/metrics` exports them for the latest test only as `emqxload_remote_connects_total{address,label}`, `emqxload_remote_active{address,label}`, `emqxload_address_label_active{label}`, `emqxload_dns_answers_total{address}` and `emqxload_dns_errors_total{resolver}`.

In CSV exports the `connect_failures` column is formatted `stage=n;stage=n`. See [Troubleshooting](troubleshooting.md#connect-failure-stages) for the usual cause of each stage.

## Security

Configured authentication protects `/api/*` and `/metrics`; probes remain accessible. Bearer clients send `Authorization: Bearer TOKEN`. Do not commit tokens. Remote deployments need TLS at the reverse proxy; the HTTP listener does not itself provide HTTPS. Browser mutations with an `Origin` header must match the request origin; native clients can omit that header.

Certificate upload accepts `name`, optional `caPem`, and paired `certPem`/`keyPem`. Listing returns metadata, not private keys. Read [certificate security](certificates.md) first. General scenario/profile deletion and role-based access controls are not implemented.
