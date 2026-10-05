# Delivery roadmap

1. Single-node CLI engine: connection/publish path, cancellation, diagnostics, benchmarks.
2. Scenario and metrics: full workload groups, MQTT 5, HDR rollups, thresholds, reconnect/churn/fault models.
3. Controller: versioned API, SQLite abstraction, SSE, restart-safe state machine.
4. Web UI: fast test wizard, live operations view, results, comparisons.
5. Distributed workers: protobuf streams, leases, clock-offset awareness, capacity scheduling.
6. EMQX integration: optional versioned adapters and broker/load correlation.
7. Deployment: hardened images, Compose, Helm, ServiceMonitor, network policy.
8. Comparative analysis: normalized run comparisons, reports, regression gates.

Every phase gates on race tests, cancellation tests, focused benchmarks, static analysis, and leak checks.

## Current delivery boundary

The single-node CLI, staged scenarios, controller/persistence, live UI, distributed execution, optional EMQX integration, deployment assets and basic comparisons are implemented. Distributed transport currently uses bounded HTTP heartbeats rather than protobuf streams. Authentication supports token, basic, OIDC and trusted reverse-proxy identity; per-user authorization is not implemented.

Remaining qualification and functionality: million-client/long-soak benchmarks, explicit generator CPU/NIC saturation telemetry, full goroutine-leak and fault matrix, reason-code error sampling, packet-phase latency, credential datasets, advanced arrival-rate models, arbitrary MQTT 5 properties, PostgreSQL, native LDAP, Kubernetes autoscaling and fault automation. Worker lease failure stops a run, but lost-capacity accounting and final metric reconciliation need additional hardening. Do not use this preview as the sole source for production capacity commitments.
