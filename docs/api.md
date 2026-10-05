# REST API

The controller serves `/api/v1`. Success responses use `{ "data": ... }`; errors use `{ "error": { "code": ..., "message": ... } }`. CSV and SSE are exceptions. This is a preview interface.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/healthz`, `/readyz` | Process probes, not broker capacity certification |
| GET | `/metrics` | Prometheus aggregates |
| POST / GET | `/api/v1/tests` | Start / list tests |
| GET | `/api/v1/tests/{id}` | Status and metadata |
| POST | `/api/v1/tests/{id}/stop` | Request cancellation |
| POST | `/api/v1/tests/{id}/rerun` | Fresh run using original server-side scenario |
| POST | `/api/v1/tests/{id}/load` | Apply [live controls](live-controls.md) |
| GET | `/api/v1/tests/{id}/metrics` | Aggregate history |
| GET | `/api/v1/tests/{id}/stream` | Live SSE |
| GET | `/api/v1/tests/{id}/export?format=json` | JSON report; `format=csv` also supported |
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

The request accepts `scenario` (YAML text) and optional `workers` (worker IDs). Omit workers for local execution. A supplied `scenarioId` selects a saved scenario instead of inline YAML. Save a scenario with `{ "name": "telemetry", "yaml": "YAML text" }`. Planning accepts `{ "scenario": "YAML text" }`.

Creation returns 201; busy conflicts return 409, invalid requests normally 400, and missing tests 404. Stop returns 202 and cannot restart a finished test. Rerun needs no scenario or secret payload:

```sh
curl --fail-with-body -sS -X POST \
  http://127.0.0.1:8080/api/v1/tests/TEST_ID/rerun
curl --fail-with-body -sS \
  'http://127.0.0.1:8080/api/v1/tests/TEST_ID/export?format=csv'
curl -N http://127.0.0.1:8080/api/v1/tests/TEST_ID/stream
```

SSE types are `status`, `metrics`, and `heartbeat`. Slow consumers can skip snapshots; there is no `Last-Event-ID` replay. Fetch state/history after reconnecting. Lists return at most 1,000 records and metrics retrieval at most 3,600 samples. JSON export contains `data.test` and `data.samples`. Go duration fields serialize as nanoseconds unless a dedicated display field is used.

## Security

Configured authentication protects `/api/*` and `/metrics`; probes remain accessible. Bearer clients send `Authorization: Bearer TOKEN`. Do not commit tokens. Remote deployments need TLS at the reverse proxy; the HTTP listener does not itself provide HTTPS. Browser mutations with an `Origin` header must match the request origin; native clients can omit that header.

Certificate upload accepts `name`, optional `caPem`, and paired `certPem`/`keyPem`. Listing returns metadata, not private keys. Read [certificate security](certificates.md) first. General scenario/profile deletion and role-based access controls are not implemented.
