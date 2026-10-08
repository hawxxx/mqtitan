# Docker and Docker Compose

Run commands from the repository root. You need Docker with Compose v2; Go and Node.js are not needed on the host because the image builds both components. The supplied stack includes a controller, a development EMQX broker, and an optional worker.

## Published images

Releases publish multi-arch (`linux/amd64`, `linux/arm64`) images to `ghcr.io/hawxxx/mqtitan` with SBOM, max-mode provenance, and a signed GitHub artifact attestation. Tags: `X.Y.Z`, `X.Y`, `X` (not for 0.x), `main`, `sha-<commit>`, and `latest` (stable `vX.Y.Z` tags only). Pin deployments by digest.

```sh
docker pull ghcr.io/hawxxx/mqtitan:0.2.0
gh attestation verify oci://ghcr.io/hawxxx/mqtitan:0.2.0 --repo hawxxx/mqtitan
# Compose with the published image instead of a local build:
MQTITAN_IMAGE=ghcr.io/hawxxx/mqtitan:0.2.0 docker compose up -d --no-build
# Helm (digest wins over tag):
helm install mqtitan deploy/helm/mqtitan --set image.digest=sha256:<digest>
```

Cut a release with `git tag v0.2.0 && git push origin v0.2.0`. The release workflow scans the image first and publishes nothing if fixable HIGH/CRITICAL vulnerabilities are found. After the first push, set the GHCR package visibility to public if you want anonymous pulls.

## Start the stack

Provide two different strong secrets in your shell or secret-management workflow. For example, in a Bash shell with OpenSSL installed:

```sh
export MQTITAN_TOKEN="$(openssl rand -hex 32)"
export EMQX_DASHBOARD_PASSWORD="$(openssl rand -hex 32)"
docker compose up --build -d
docker compose ps
docker compose logs --tail=50 controller
```

Keep these variables available for later Compose commands and subsequent startups. Do not commit them. Open http://127.0.0.1:8080, go to **Settings**, and enter `MQTITAN_TOKEN`. The browser keeps it for the session. The EMQX development dashboard is at http://127.0.0.1:18083; its password is the separate EMQX secret.

## Run a first test

In **Tests → Create test**, use these initial values:

| Field | Value |
| --- | --- |
| Broker | `mqtt://broker:1883` |
| Clients | `100` |
| Publish rate | `1` message/sec/client |
| Payload | `128` bytes |
| Duration | `30s` |

Run the test, watch the live screen, then open Results to inspect thresholds and export a report. Localhost inside the controller container is not the broker: use the Compose service name `broker`.

Alternatively submit using the CLI already inside the container:

```sh
docker compose exec controller mqtitan quick \
  --controller http://127.0.0.1:8080 --broker mqtt://broker:1883 \
  --clients 100 --rate 1 --duration 30s
docker compose exec controller mqtitan status --controller http://127.0.0.1:8080
```

The CLI inherits the controller's bearer token. The server stays running after the submitted test finishes.

## Add a worker

```sh
docker compose --profile workers up -d
docker compose exec controller mqtitan workers --controller http://127.0.0.1:8080
docker compose exec controller mqtitan quick \
  --controller http://127.0.0.1:8080 --workers compose-worker-1 \
  --broker mqtt://broker:1883 --clients 100 --rate 1 --duration 30s
```

The UI also lets you select the registered worker during test creation. The supplied service has a fixed worker ID: do not use `--scale worker=N` unchanged, because replicas would register the same ID. Additional services need unique IDs; use the Helm deployment for multiple automatically named pods.

## Test an external broker or load balancer

Replace the test's broker URL with a hostname reachable from the executing container, such as `mqtts://broker.example.com:8883` or `wss://load.example.com:443/mqtt`. An HTTPS load balancer must forward WebSocket upgrades to a broker WebSocket listener; `https://` is not a valid MQTT transport URL.

Upload custom CA or client certificate/key files in the wizard's TLS section. Profiles are reusable encrypted database records. Uploading does not change the image or require rebuilding it. See [certificates](certificates.md) for TLS termination and persistence details.

For controller/worker-only deployments, build `docker build -t mqtitan:local .` and use an equivalent Compose configuration without the `broker` service. The supplied worker service depends on the demo broker; simply removing that service without updating dependencies is not sufficient.

## Stop, restart, and preserve results

```sh
docker compose --profile workers down
# Later, with the same secrets available:
docker compose --profile workers up -d
```

Named volumes preserve results, encrypted certificate profiles, and broker state. `down` does not erase them. Avoid `down -v`: it deletes the stack's stored data. Back up the controller database and adjacent `.key` together; see [operations](operations.md#persistence-and-recovery). Stop active tests before shutdown; a controller restart cannot restore their MQTT sockets.

The demo broker permits unauthenticated MQTT and is not a production configuration. Keep host bindings on loopback, use authenticated HTTPS for remote control-plane access, and qualify resource/descriptor limits before increasing load. See [troubleshooting](troubleshooting.md).
