# Worker replacement and controller resume (design)

Status: proposal. Builds on `maxLostWorkers` (a test survives lost workers but their clients are not replaced).

## Goal

A distributed test keeps close to its requested load when a worker is lost, and survives a controller restart.

## Phase 1: replace a lost worker

When a tolerated lost worker is detected (`checkWorkers`), give its partition to another worker.

- Partition identity is already `ClientOffset` + `CapacityClients` + the partition scenario (`assign`). A replacement receives a copy with a new assignment ID, the same offset and capacity, and the current live-load partition.
- Candidates are registered, ready, unassigned workers. Opt-in with `replaceLostWorkers` (default off), choosing from a pool: explicit `standbyWorkers` list, or any ready worker not in the test.
- The replacement connects fresh. Per-client MQTT session state is not transferred; reconnecting clients are a visible burst on the broker, so ramp the replacement using the scenario's connect rate rather than `StartAt` immediately.
- Accounting: the lost worker keeps its final counters as history (existing behaviour). The replacement reports under its own ID, and `workerIds` / `lostWorkers` plus a new `replacedWorkers: [{lost, by, at}]` record show what happened. Merge stays additive because counters are cumulative per worker.
- `workerSnapshot` iterates `r.workerIDs`; replacements must be appended under the registry lock. Replaced losses do not count toward the tolerance (implemented this way so replacement works with `maxLostWorkers` 0); unreplaced ones do.
- Limits: at most one replacement per lost worker, and none after the run is stopping. If no candidate exists, behave as today (tolerate and continue degraded).
- Kubernetes: a restarted pod registers with a new ID (or the same ID after the lease expired) and becomes a candidate automatically, so no scheduler integration is needed.

Tests: replacement takes the exact offset/capacity; no candidate keeps running degraded; replacement lost in turn; live load change reaches the replacement; result lists replacements.

## Phase 2: controller resume

Today a restart marks active tests `interrupted` and workers drop their runs.

- Persist per test: scenario, worker assignments (ID, offset, capacity, load revision), worker sessions, per-worker last report. Persist on assignment, on load changes, and at a low rate for reports.
- On startup, rehydrate runs whose workers may still be alive and enter a grace period (a few leases). Workers keep generating load, retry heartbeats, and re-register presenting their previous session; the controller accepts a session it persisted.
- Reports are cumulative with per-assignment sequence numbers, so a resumed controller merges them idempotently. Workers not back by the end of the grace period are treated as lost (Phase 1 rules).
- Workers must not stop on `errMustRegister` for an unfinished assignment during the grace period.
- Risk: sessions and credentials become persisted state; assignment contents (broker credentials, TLS keys) must stay encrypted like other records, and the single-controller deployment model is unchanged (no HA).

Tests: restart mid-run with workers alive; restart with workers dead; stale session rejected after grace.

## Out of scope

Transferring live MQTT sessions between workers, multi-controller HA, and automatic Kubernetes scaling.
