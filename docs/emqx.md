# Optional EMQX observations

The adapter reads GET /api/v5/nodes and GET /api/v5/metrics?aggregate=false with HTTP Basic authentication: username is an EMQX API key, password its secret. Dashboard administrator credentials are not API keys. Keep credentials in runtime environment/secret references and prefer HTTPS.

Client.Fetch returns a timestamped Snapshot and an error. An endpoint failure leaves available observations intact; callers must display the error instead of treating missing counters as zero. Client.Poll continues through API failures until its context is canceled. Run polling independently of the MQTT execution path; its receiver should return promptly.

Connections are summed only when every returned node supplies live_connections. The whitelist includes messages.received, messages.sent, messages.dropped, messages.publish, bytes.received, bytes.sent, packets.received and packets.sent. Missing metrics are omitted. Message totals are cumulative broker counters, not per-second rates; resets require explicit handling by downstream charts.

Node cpu_use is exposed only when supplied, without rescaling. memory_used preserves the API value and units. The adapter does not guess CPU values from load averages or convert memory strings into aggregate byte counts. Nodes and metrics are separate requests rather than an atomic broker snapshot.

The flat per-node metric response was checked against the official [EMQX 5.8.8 source](https://github.com/emqx/emqx/blob/v5.8.8/apps/emqx_management/src/emqx_mgmt_api_metrics.erl) and [EMQX 6.0.0 source](https://github.com/emqx/emqx/blob/e6.0.0/apps/emqx_management/src/emqx_mgmt_api_metrics.erl). Broker upgrades should recheck response compatibility and API-key permissions.

Requests verify TLS by default, reject redirects, have a five-second maximum timeout and cap each body at 1 MiB by default (configurable up to 8 MiB). Errors exclude credentials, response bodies and URLs. A custom HTTP client can install private CA trust; do not disable verification. This integration is optional: unavailable broker APIs must never stop a load test.
