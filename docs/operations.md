# Deployment and operation

For step-by-step setup and a first test, use [Docker and Compose](docker.md) or [Kubernetes with Helm](kubernetes.md). This guide covers additional operational settings and qualification.

## Local quickstart

Build the CLI with Go 1.25 and the UI with Node 22:

```sh
make build
./bin/mqtitan serve --listen 127.0.0.1:8080 --data ./mqtitan.db
```

Open http://127.0.0.1:8080. Local mode binds loopback. Set MQTITAN_TOKEN to enable bearer authentication. Non-loopback serving requires configured authentication; --allow-remote-local-mode explicitly enables an unauthenticated demo and should only be used on an isolated trusted network. The build embeds the frontend; `--web web/dist` is an optional filesystem override.

For the container demo, export MQTITAN_TOKEN and EMQX_DASHBOARD_PASSWORD as strong random values, then run:

```sh
docker compose up --build -d
docker compose --profile workers up -d
```

The controller is available at http://127.0.0.1:8080, MQTT at localhost:1883, and the EMQX dashboard at http://127.0.0.1:18083. EMQX is a development broker with unauthenticated MQTT access on the isolated Compose network; configure broker authentication and TLS before other deployments. Broker URLs submitted to the container controller must use mqtt://broker:1883. localhost inside a container points to that container.

Volumes persist controller and broker state. Stop with docker compose down; do not add -v unless deleting all stored results is intended. Workers use unique IDs; the sample starts one worker. A single SQLite controller is intentional. Do not scale its replicas above one or share its database with independent processes.

## Helm

Build and push an image under a versioned tag. Create a Kubernetes Secret named mqtitan-auth containing the token key through your secret-management workflow. Do not put real credentials in committed values files.

```sh
helm lint deploy/helm/mqtitan
helm template mqtitan deploy/helm/mqtitan --set image.repository=registry.example.com/mqtitan
helm upgrade --install mqtitan deploy/helm/mqtitan \
  --set image.repository=registry.example.com/mqtitan \
  --set image.tag=0.1.0 --set auth.existingSecret=mqtitan-auth
kubectl port-forward service/mqtitan 8080:8080
```

The chart defaults to one controller, a 10 GiB RWO volume, zero workers, resource limits, a non-root read-only filesystem, a controller disruption budget and a NetworkPolicy. Configure worker.replicas after assigning resources and allowing broker egress. DNS and worker-to-controller traffic are allowed; broker traffic is denied until networkPolicy.brokerEgress is set. NetworkPolicy requires a supporting CNI. Example:

```yaml
worker:
  replicas: 3
  topologySpreadConstraints:
    - maxSkew: 1
      topologyKey: topology.kubernetes.io/zone
      whenUnsatisfiable: DoNotSchedule
      labelSelector:
        matchLabels:
          app: mqtitan
          component: worker
networkPolicy:
  brokerEgress:
    - to:
        - ipBlock:
            cidr: 10.20.0.0/24
      ports:
        - protocol: TCP
          port: 8883
```

worker.nodeSelector and worker.affinity also accept native Kubernetes configuration. Zone spreading requires nodes carrying zone labels. Place workers near brokers when measuring broker capacity, and deliberately vary zones when measuring network effects.

Ingress is optional. Configure ingress.className, ingress.host and ingress.tlsSecret to terminate HTTPS; bearer tokens must travel over encrypted connections. The controller has /healthz and /readyz probes on port 8080. Workers expose the same paths on port 8081 via --health-listen; readiness reflects controller communication. Controller heartbeats and leases separately detect unavailable execution capacity.

serviceMonitor.enabled requires Prometheus Operator CRDs. It scrapes /metrics using the same Secret bearer credential. Configure Prometheus's ServiceMonitor selector to include this release. The single-controller PDB blocks voluntary eviction while that controller is running; coordinate node maintenance, backups and upgrades, and expect downtime during the Recreate rollout.

## Persistence and recovery

Keep the SQLite database, WAL and SHM files on the same writable persistent filesystem. Back up using SQLite's online backup facilities or stop the controller cleanly before copying the database. A plain copy of a live database without its WAL can omit recent transactions. Exercise restores before relying on backups. Monitor disk space and external retention requirements; unlimited run history can exhaust the volume.

Restart behavior must preserve stored results and mark unfinished runs interrupted. A restart cannot reconstruct live MQTT sockets. Worker loss does not prove broker failure; inspect worker heartbeat status and generator saturation alongside broker metrics.

## Security

MQTITAN_TOKEN authorizes the control plane; treat it as an administrative secret, rotate it on the controller and all workers together, and do not place it in URLs. Local mode is for trusted single-user machines. Basic authentication uses MQTITAN_BASIC_USERNAME and MQTITAN_BASIC_PASSWORD. OIDC bearer-token verification uses MQTITAN_OIDC_ISSUER and MQTITAN_OIDC_CLIENT_ID. Trusted reverse-proxy identity uses MQTITAN_PROXY_IDENTITY_HEADER plus MQTITAN_TRUSTED_PROXY_CIDRS; never trust identity headers from arbitrary clients. This release does not provide per-user RBAC or native LDAP. Persistent test/scenario records are encrypted with AES-GCM; back up the database and its adjacent .key file together. Aggregated time-series samples are not encrypted. Broker passwords remain sensitive input: use runtime injection, avoid committing them, and verify redaction on exported results.

TLS connections use certificate verification. Install private CA certificates into the container trust store when necessary; avoid disabling verification. Broker listener TLS and control-plane HTTPS are separate settings. API access permits load generation against broker destinations, so restrict network egress and control-plane access.

Do not expose EMQX development dashboard ports to public networks. The container drops Linux capabilities and writes only its data volume and temporary directory. Image tags in the examples are versioned; for reproducible releases pin base and deployed images to verified digests in your release pipeline.

## Linux worker sizing

Run mqtitan doctor on each worker host. Measure open file descriptors, resident memory, CPU, NIC throughput, dropped packets, connection ramp accuracy, publish pacing and TLS handshake cost. Set process/container file descriptor limits before startup; raising a host limit does not automatically raise the container's limit. Compose uses 65,536 as an initial ceiling, which is insufficient for some large-population runs.

Doctor reads CPU count, available host memory, the process descriptor limit, ephemeral-port range, TCP receive/send buffer settings, TIME_WAIT reuse and socket counts, optional conntrack occupancy, DNS nameserver count, and NIC link speed/byte/drop counters. DNS addresses and search domains are redacted. NIC and drop counters are cumulative; take timed samples to identify changes during the run. Virtual interfaces can report no usable speed. Conntrack may be unavailable in the worker namespace. Doctor does not write sysctls or change process limits.

The capacity helper compares a plan against measured descriptor, port, memory and link-speed ceilings. Its NIC estimate uses the fastest reported interface and does not infer the interface routed to the broker; verify the actual path. Host available memory does not replace Kubernetes/container memory limits. Its port budget assumes one source IP and one destination tuple unless the caller supplies the actual counts. An unknown ceiling is reported as unknown, and an estimate below a ceiling is not a qualified performance guarantee. CPU count is reported without an invented clients-per-core estimate.

Inspect ulimit -n, /proc/sys/net/ipv4/ip_local_port_range, ss -s, and the host's memory and network statistics. One source IP has a finite ephemeral-port range for each destination tuple. Hundreds of thousands of connections to the same broker address require multiple worker hosts/source IPs or additional destination addresses; NAT may introduce another bottleneck. TIME_WAIT and connection churn can exhaust ports before steady-state connection limits.

Change sysctls through your host configuration after measuring a bottleneck. Do not blindly enable port reuse or disable TCP safety settings. Reserve file descriptors for logs, HTTP and DNS in addition to MQTT sockets. Reserve memory for the runtime and broker/TLS buffers, and leave CPU headroom for telemetry and the operating system. Kubernetes resource limits and node/container ulimits are independent.

The planner's memory and bandwidth figures are estimates, not admission-control guarantees or benchmarks. Current client-library transport allocates goroutines and buffers per connection. A million-client example requires capacity qualification on specified hardware and cannot certify this transport at that scale. Distribute progressively: 1k, 10k, 100k, then higher only after the previous step meets pacing, error and resource criteria.

## CI and qualification

CI runs Go race tests, vet, UI build, chart lint/render and Docker build. It checks software correctness; it does not certify benchmark capacity. Run a small scenario against an isolated real broker before upgrades:

```sh
mqtitan validate examples/ci-smoke.yaml
mqtitan plan examples/ci-smoke.yaml
mqtitan run examples/ci-smoke.yaml
```

A failed threshold must return a nonzero exit status. Preserve result artifacts and compare achieved rate, latency and errors against the same hardware/broker configuration. QoS 0 publisher latency measures client send completion; QoS 1 and 2 include protocol acknowledgment overhead. Generator saturation invalidates a broker-capacity conclusion even if the requested client count was reached.
