# Engineering preview verification

Verified locally on 2026-10-04. These results are smoke/regression evidence, not an independent capacity certification.

- `go test -race -timeout 90s ./...`: passed all tested packages.
- `go vet ./...`: passed.
- `npm test -- --run`: 12 tests passed in three files.
- `npm run build`: TypeScript and Vite production build passed.
- Real-wire regression tests cover MQTT 3.1.1/5, TLS trust/SNI, WebSocket delivery, staged populations, reconnect, operation timeout, cancellation and two-worker execution.

## EMQX 10,000-client smoke run

Broker: local Docker EMQX 5.8.8. MQTT 5, QoS 1, 512-byte random payloads, one message/client/second, 30-second total duration including the approximately 20-second connection ramp. Local generator and broker share the host; latency is not representative of a remote production deployment.

```sh
bin/mqtitan quick --broker mqtt://127.0.0.1:18884 --mqtt-version 5 \
  --clients 10000 --rate 1 --qos 1 --payload-size 512 --duration 30s
```

Run ID: `45b514282bc977cd4a0b182d`.

Peak connected: 10,000. Steady observed publish rate: approximately 10,000 messages/sec. Successful publishes: 194,594. Connection/publish errors: zero. Final publish p95: 1.117 ms; p99: 1.898 ms. Generator exited successfully and closed all clients.

An earlier run exposed intentional shutdown cancellation being counted as publish failure. The fix distinguishes canceled operations from broker failures and is covered by MQTT 3.1.1/5 regression tests.

The deployment image/chart and browser workflow were checked during implementation; rerun their checks for your target environment. Million-client performance, extended soak/failure tests, comprehensive leak auditing and third-party security review remain release gates. See the roadmap for unsupported advanced features.

## Certificate UI extension

The test wizard now supports CA/client-certificate/private-key uploads, encrypted reusable profiles, and SNI. The frontend suite passes 17 tests. Full Go race tests and static analysis pass after the extension. Real socket integration tests exercise uploaded profiles over WSS with mutual TLS, using MQTT 3.1.1 and MQTT 5, both locally and through a distributed worker. Tests also verify invalid/mismatched uploads, profile persistence across restart, ciphertext on disk and private-key redaction from reports.

A real browser successfully uploaded a temporary verification CA through the wizard and selected the resulting profile. Its generated scenario contains only the profile ID, not private material. Browser artifacts are local under `output/playwright/` and excluded from version control.

## Live load controls

The frontend suite passes 22 tests, the production build succeeds, and `go vet ./...` and the full Go race suite pass. Integration tests cover live client changes and publish pause/resume with MQTT 3.1.1 and MQTT 5, including two real distributed workers, revision acknowledgements, cancellation, bounds and durable change history. The unchanged-control benchmark reports zero allocations; this is not a large-scale capacity qualification.

A browser run against the local EMQX broker used capacity 20 and initially connected 2 clients. Applying 10 clients at 10 messages/publisher/sec connected 10; applying 3 clients at rate 0 disconnected excess clients and paused publishing. A keyboard slider adjustment to 4 clients and rate 5 resumed publishing. Stop closed all connections with zero connection/publish errors. Three timestamped revisions remain in the result. The screenshot is `output/playwright/live-controls.png`.

Live changes require Apply, stay within the configured client capacity, override later client stages, and do not extend the original duration. See [live controls](live-controls.md) for distributed propagation and limits.

## Slider and visual refinement

The Magic UI MCP registry was queried for suitable components. Its static Grid Pattern was adapted to the existing CSS stack, without Tailwind or animation-library dependencies; attribution is in `THIRD_PARTY_NOTICES.md`. The dashboard introduction and empty state use the pattern. Shared styling improves contrast, focus states, navigation selection, numeric alignment and reduced-motion behavior.

Sliders now have filled tracks, larger handles, scale ticks, logarithmic publisher-rate control, presets, exact numeric inputs, unapplied-change status and Reset draft. The 29-test frontend suite passes, including scale conversion, accessible values, disabled controls, draft/reset behavior and unique decorative SVG IDs. The final production frontend and embedded Go binary build successfully.

Browser checks covered all eight main pages at 1440px desktop and 390px mobile widths, with no document-level horizontal overflow. Dark/light screenshots are under `output/playwright/design-*.png`. A real MQTT 5/QoS 1 EMQX run confirmed presets remain drafts until Apply, pause/resume and clean shutdown. A final keyboard check increased the client target from 4 to 5 and applied 10 messages/publisher/sec; the broker reached 5 connected clients. Verification runs were stopped after checking.

## Run again

The frontend suite passes 33 tests; the full Go race suite, `go vet ./...`, production frontend build and embedded binary build pass. Rerun tests cover terminal-status validation, authentication, active-test conflicts, missing results, immutable source records/samples, reset metrics/live overrides, server-side credential reuse, controller restart, recorded distributed worker assignment and rejection of unavailable workers.

A browser clicked Run again on stopped source `b90e059144357823f6e0c214` and navigated to fresh run `14a227095c821c9b5980f86f`. EMQX reached 4 clients at the original rate 1; the previous manual 5-client/rate-10 override was not inherited. The old result remained stopped with 561 publishes and its original change history. Stopping the new run made Run again available without a reload. The local browser artifact is `output/playwright/rerun-live.png`.

The older source has no saved worker metadata, and its local-execution warning was visible before rerunning. New runs persist worker selection and source lineage. See [rerunning tests](reruns.md) for semantics and legacy behavior.
