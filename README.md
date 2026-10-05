# MQTTitan

MQTTitan is an open-source MQTT load-testing platform for EMQX and standards-compatible brokers, with a Go CLI/controller and a React operations dashboard. This is a working engineering preview, not a certified production release.

## Quick start

```bash
make build
./bin/mqtitan quick --broker mqtt://localhost:1883 --clients 1000 --rate 1 --duration 30s
./bin/mqtitan validate examples/10k.yaml
./bin/mqtitan plan examples/10k.yaml
./bin/mqtitan run examples/10k.yaml
./bin/mqtitan doctor
```

During a local run, open http://127.0.0.1:8080 to watch the same test. Results persist in SQLite. Run `./bin/mqtitan serve` to browse saved runs afterward. `quick --save scenario.yaml` saves a reusable scenario. Building from source requires Go and Node.js; the built binary includes the dashboard.

For containers, follow [operations](docs/operations.md) to configure required secrets, then run `docker compose up --build`. Distributed workers, Helm deployment, authentication, CI thresholds, reports, and host tuning are documented there.

The test wizard supports [certificate uploads](docs/certificates.md): custom CA trust, client certificates/private keys for mTLS, reusable encrypted profiles, and SNI overrides for secure MQTT or WebSocket endpoints.

Use [live load controls](docs/live-controls.md) to adjust client targets and message rates during a test. Set spare client capacity in the wizard, then use sliders and **Apply load** on the live test screen. Local and distributed execution share the same controls; changes are recorded in results.

See [architecture](docs/architecture/overview.md), [scenario specification](docs/scenario-spec.md), and [roadmap](docs/roadmap.md).

## Status

Implemented: MQTT 3.1.1/5 over TCP/TLS/WebSocket, publisher/subscriber/mixed workloads, staged client populations, bounded reconnects, dynamic payloads, HDR latency metrics, end-to-end correlation, encrypted scenario storage, REST/SSE, live dashboards, comparisons, distributed partitions, optional EMQX telemetry, Prometheus, OpenTelemetry, Docker and Helm.

Production release gates remain: independently calibrated large-scale capacity, million-client testing, extended fault/soak coverage, and security review. Advanced requirements not yet implemented include credential datasets, global/Poisson/burst rate models, arbitrary MQTT 5 properties, native LDAP, PostgreSQL, automatic Kubernetes scaling, and external fault orchestration. Do not interpret a supplied large scenario as proven generator capacity. `plan` estimates are explicitly uncalibrated; use `doctor` and measure worker resource limits before scaling.

Back up the database **and its adjacent `.key` file** together. Losing the key makes encrypted records unreadable. Never commit either file. Local unauthenticated mode defaults to loopback; remote deployments must configure authentication and TLS at the ingress/reverse proxy.
