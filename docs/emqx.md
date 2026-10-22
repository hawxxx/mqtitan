# Optional EMQX observations

## Enable broker telemetry

Set these variables in the controller's runtime environment before starting `mqtitan serve`:

```sh
export MQTITAN_EMQX_URL=https://emqx-management.example.com:18084
# Supply MQTITAN_EMQX_API_KEY and MQTITAN_EMQX_API_SECRET through your secret manager.
./bin/mqtitan serve
```

The URL is the management server base URL, not the MQTT endpoint or a path ending in `/api/v5`. Use the actual management port configured on your deployment. The dashboard's Brokers section and `GET /api/v1/brokers/emqx` expose observations. This integration is optional and does not configure broker authentication for MQTT clients.

## Supported observations and limitations

The adapter reads GET /api/v5/nodes and GET /api/v5/metrics?aggregate=false with HTTP Basic authentication: username is an EMQX API key, password its secret. Dashboard administrator credentials are not API keys. Keep credentials in runtime environment/secret references and prefer HTTPS.

Client.Fetch returns a timestamped Snapshot and an error. An endpoint failure leaves available observations intact; callers must display the error instead of treating missing counters as zero. Client.Poll continues through API failures until its context is canceled. Run polling independently of the MQTT execution path; its receiver should return promptly.

Connections are summed only when every returned node supplies live_connections. The whitelist includes messages.received, messages.sent, messages.dropped, messages.publish, bytes.received, bytes.sent, packets.received and packets.sent. Missing metrics are omitted. Message totals are cumulative broker counters, not per-second rates; resets require explicit handling by downstream charts.

Node cpu_use is exposed only when supplied, without rescaling. memory_used preserves the API value and units. The adapter does not guess CPU values from load averages or convert memory strings into aggregate byte counts. Nodes and metrics are separate requests rather than an atomic broker snapshot.

The flat per-node metric response was checked against the official [EMQX 5.8.8 source](https://github.com/emqx/emqx/blob/v5.8.8/apps/emqx_management/src/emqx_mgmt_api_metrics.erl) and [EMQX 6.0.0 source](https://github.com/emqx/emqx/blob/e6.0.0/apps/emqx_management/src/emqx_mgmt_api_metrics.erl). Broker upgrades should recheck response compatibility and API-key permissions.

Requests verify TLS by default, reject redirects, have a five-second maximum timeout and cap each body at 1 MiB by default (configurable up to 8 MiB). Errors exclude credentials, response bodies and URLs. A custom HTTP client can install private CA trust; do not disable verification. This integration is optional: unavailable broker APIs must never stop a load test.

## Per-test telemetry

A scenario can poll broker connection counts during the test and keep them with the test record. The poller runs only on the controller process; workers never contact EMQX or Prometheus. Set exactly one source:

```yaml
telemetry:
  interval: 15s          # default 15s, minimum 5s
  expectedTargets: 3     # 0 disables the check
  baseline: first        # "", none, or first
  emqx:
    - { name: emqx-0, zone: zone-a, url: "https://emqx-0.example.net:18084" }
    - { name: emqx-1, zone: zone-b, url: "https://emqx-1.example.net:18084" }
# or
#  prometheus:
#    url: https://prometheus.example.net:9090
#    query: sum by (pod, zone) (emqx_live_connections_count)
#    targetLabel: pod    # default
#    zoneLabel: zone     # default
```

Credentials are never in YAML and URLs with user information are rejected. The controller reads `MQTITAN_EMQX_API_KEY` and `MQTITAN_EMQX_API_SECRET` for `emqx` targets, and `MQTITAN_PROMETHEUS_TOKEN` (bearer) or `MQTITAN_PROMETHEUS_USERNAME` and `MQTITAN_PROMETHEUS_PASSWORD` for Prometheus.

Each sample lists every target's `Connections` (after baseline subtraction, clamped at 0) and `Raw` value. `Complete` is false when a target failed, a Prometheus series lacked the target label, or fewer targets than `expectedTargets` reported; more targets than expected is not a failure. With `baseline: first`, one sample is taken before the load starts (flagged `Baseline`), and later samples subtract each target's first raw value; targets absent from it count from 0. Requests poll at most 16 targets concurrently, time out after min(interval/2, 5s), refuse redirects and cap responses at 1 MiB. At most 2880 samples are kept per test. Read them with `GET /api/v1/tests/{id}/brokers` or the `broker` stream event. The global `MQTITAN_EMQX_URL` telemetry above is independent.
