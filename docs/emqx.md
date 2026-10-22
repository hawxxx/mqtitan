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

Choose the source by fleet size. The `emqx` target list is capped at 16 concurrent requests and is meant for local or compose use with a handful of brokers; Prometheus is preferred for hundreds of pods. EMQX 4.x `/api/v4` targets are not supported; use Prometheus for those fleets.

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

Each `emqx` target must address exactly one broker node, for example a per-pod or per-container management URL. `GET /api/v5/nodes` lists every node of a cluster, so a clustered target or a Service URL that lands on random pods would report fleet-wide totals and hide any imbalance. A target that reports more than one node is marked with `target reports N nodes; one node per target required`, and two targets that report the same node are both marked with `targets A and B report the same node <name>`; either makes the sample incomplete. Each target sample lists the node names it saw in `nodes`. For clustered fleets use the Prometheus source with one series per pod.

Each sample lists every target's `Connections` (after baseline subtraction, clamped at 0) and `Raw` value. `Complete` is false when a target failed, a Prometheus series lacked the target label, or fewer targets than `expectedTargets` reported; more targets than expected is not a failure. With `baseline: first`, one sample is taken before the load starts (flagged `Baseline`), and later samples subtract each target's first raw value; targets absent from it count from 0. Requests poll at most 16 targets concurrently, time out after min(interval/2, 5s), refuse redirects and cap responses at 1 MiB. At most 2880 samples are kept per test. Read them with `GET /api/v1/tests/{id}/brokers` or the `broker` stream event. The global `MQTITAN_EMQX_URL` telemetry above is independent.

### Prometheus queries and zones

EMQX 5.8.8 serves `/api/v5/prometheus/stats` with both `emqx_connections_count` and `emqx_live_connections_count`, without labels, so `pod` and `zone` come from your scrape configuration (see `deploy/e2e/balance/prometheus.yml` for static labels). Prefer `emqx_live_connections_count`. On Kubernetes the zone comes from the node, not from EMQX; join it from kube-state-metrics:

```promql
sum by (pod, zone) (emqx_live_connections_count * on (pod) group_left(node) kube_pod_info{namespace="emqx"} * on (node) group_left(zone) label_replace(kube_node_labels, "zone", "$1", "label_topology_kubernetes_io_zone", "(.*)"))
```

While a pod is being replaced `kube_pod_info` can briefly hold two series for one pod and the join fails with a many-to-many error. Use `max by (pod, node) (kube_pod_info{namespace="emqx"})` in its place to avoid it. kube-state-metrics only exports node labels listed in its `metricLabelsAllowlist` (`nodes=[topology.kubernetes.io/zone]`); without it the zone is empty and `zone_connection_skew` fails with `no observations`.

Prometheus cannot report a pod it never scraped, so set `expectedTargets` to the number of brokers; otherwise a missing pod goes unnoticed, and `validate` and `plan` warn when a balance threshold is configured. `baseline: first` judges balance on the test's own connections when other clients share the brokers. The poller runs on the controller, so the controller needs network access to Prometheus (for example `kubectl port-forward`), while workers can run elsewhere. The end-to-end proof is `deploy/e2e/balance/run.sh`.

### Allowed telemetry hosts

Scenarios submitted through the API or dashboard may only poll hosts listed in `MQTITAN_TELEMETRY_ALLOWED_HOSTS`, a comma-separated list of hostnames or `host:port` values (exact, case-insensitive; a bare hostname matches any port). With the variable empty, API-submitted telemetry is rejected with `telemetry host <host> is not in MQTITAN_TELEMETRY_ALLOWED_HOSTS`. This keeps submitted YAML from sending the controller's EMQX or Prometheus credentials to arbitrary hosts. Local runs (`mqtitan run` without `--controller`) are exempt, since the same user supplies the YAML and the credentials. Reruns of local tests go through the allowlist, since a rerun is an API submission. Set the variable on the controller (for example the Compose `environment` block) whenever scenarios with telemetry are submitted through the API or `--controller`. Entries are matched as follows: IPv6 literals are written bare (for example `::1`) without brackets, and a URL host with a trailing dot does not match its entry. Per-test polling uses the default HTTP client, so telemetry endpoints behind a private CA are not supported yet. Samples are stored apart from the test record, so test lists stay small.
