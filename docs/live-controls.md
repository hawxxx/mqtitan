# Live load controls

The control panel provides styled, keyboard-accessible sliders with filled tracks, scale ticks and exact numeric inputs. Client presets select 25%, 50% or 100% of the configured capacity; Disconnect selects zero. Publisher presets include Pause, 1, 10 and 100 messages/publisher/sec. Presets only edit your draft: click **Apply load** to send a request. **Reset draft** restores the latest controller values without changing traffic.

Open a running test from **Live Tests**. The **Live load controls** panel provides client and message-rate sliders plus exact-number inputs. Move controls, then select **Apply load**. Dragging alone never changes traffic.

## Set capacity before launch

The test wizard has two client settings:

- **Clients**: initial connection target.
- **Maximum clients (live controls)**: the largest target allowed during this test. It defaults to Clients.

For example, start with 1,000 clients and a maximum of 10,000. You can then request any target from zero to 10,000 without changing client identities or launching another test. Capacity is a configuration limit, not a certification that a worker can generate that load. Review generator resources before increasing traffic. Capacity planning for the original stages does not reserve resources for later manual increases.

In YAML, set `clients.count` and workload populations to the maximum capacity, and set the initial stage's `targetClients` lower.

## Semantics

- Client target increases establish additional connections using the existing bounded connection ramp. Decreases cancel selected clients and close their connections. Zero clients disconnects the entire population.
- Message rate is **messages per publisher per second**, not a global message rate. Applying a request sets this rate for every publisher/mixed workload group. Subscriber-only groups do not publish.
- Zero message rate pauses publishing while keeping clients connected. Publishing resumes when a positive rate is applied.
- Applying any live request switches to manual control. Subsequent scenario stages cannot overwrite the requested client target. The original test duration remains unchanged. There is currently no button to return to automatic stages; launch a new run to replay the original schedule.
- Client identities, topic templates, payload generation, subscriptions and workload memberships remain unchanged. Targets select a prefix of the existing global client population, just as the original stage scheduler does; live changes do not rebalance group percentages.
- The rate slider uses a logarithmic scale for useful low-rate precision. Number inputs accept exact values. The engine's upper rate bound is 1,000,000 messages/publisher/second; this is a validation limit, not guaranteed throughput.
- Every accepted request is timestamped, revisioned and persisted before it reaches the engine. The original scenario is unchanged. Recent requests appear in the panel; full request history appears in JSON reports and remains visible after the run ends. CSV exports remain metric samples only.
- Updates are limited to 1,000 requests per test and at least 250 ms between requests. Revision checks reject stale concurrent edits instead of silently overwriting them.

## Distributed tests

The controller partitions each requested global client target using the original worker ranges and sends changes over existing authenticated heartbeats. Publishers use a shared in-memory notification, not per-client polling or unbounded command queues.

Worker delivery/acknowledgement normally takes approximately one to two heartbeat cycles. The UI shows pending worker acknowledgements; connected-client metrics show progress toward the target. Acknowledging a request does not mean the broker has established every requested connection yet. Rapid commands may be coalesced so workers apply the latest target; history records requests, not a guarantee that every intermediate target was sustained.

Upgrade the controller and all workers together. The controller refuses live updates if an assigned worker is unavailable, finished or does not advertise live-control support. Browser disconnection never stops load generation.

## API

```http
POST /api/v1/tests/TEST_ID/load
Content-Type: application/json
Authorization: Bearer CONTROL_PLANE_TOKEN

{"clients":5000,"ratePerClient":2,"revision":0}
```

Use the current `loadControl.revision` from `GET /api/v1/tests/TEST_ID`. Success returns updated test metadata and the next revision. HTTP 400 indicates invalid/missing fields or unsupported workers; 409 indicates stale revision or an inactive test; 429 indicates update limits. The endpoint uses existing authentication, request limits and same-origin protection.
