# Kubernetes with Helm

This guide deploys MQTTitan, not an EMQX cluster. You need a reachable MQTT broker, Docker/image-registry access, kubectl configured for your cluster, Helm 3, a persistent-volume provisioner, and a CNI that enforces NetworkPolicy. Run commands from the repository root.

The preview uses one SQLite controller. Do not add controller replicas or share its database with another controller. Workers scale independently, but worker autoscaling and transparent failover are not implemented.

## Build an image and create authentication

Replace the registry and tag with an image your nodes can pull:

```sh
docker build -t registry.example.com/team/mqtitan:docs-demo .
docker push registry.example.com/team/mqtitan:docs-demo
kubectl create namespace mqtitan
```

Provision a Secret named `mqtitan-auth` in that namespace with a `token` key through your secret-management workflow. For a local administrative shell with an already supplied `MQTITAN_TOKEN`:

```sh
kubectl -n mqtitan create secret generic mqtitan-auth \
  --from-literal=token="$MQTITAN_TOKEN"
```

Do not use an empty token, commit the Secret, or expose it in CI logs. This command briefly passes the secret as a process argument; prefer your organization's secret operator for production. The chart currently has no imagePullSecrets value: configure registry access through your cluster's image-pull mechanism before installing a private image.

## Configure worker capacity and broker egress

Create a local `values.local.yaml` with the following structure. Replace the image and example broker CIDR/port with your actual endpoint network:

```yaml
image:
  repository: registry.example.com/team/mqtitan
  tag: docs-demo
auth:
  existingSecret: mqtitan-auth
worker:
  replicas: 2
networkPolicy:
  brokerEgress:
    - to:
        - ipBlock:
            cidr: 10.20.0.0/24
      ports:
        - protocol: TCP
          port: 8883
```

NetworkPolicy defaults deny broker traffic until this egress is supplied. DNS and worker-to-controller traffic are already allowed. For WSS use the actual destination TCP port, usually 443. Standard NetworkPolicy uses IP/pod selectors, not DNS names; a load balancer with changing addresses needs a maintained policy or your CNI's DNS-aware mechanism. Optional EMQX management polling also needs egress to its API endpoint. Review service/NAT behavior with your CNI rather than assuming a CIDR rule matches every path.

Inspect [chart values](../deploy/helm/mqtitan/values.yaml) for worker resources, nodeSelector, affinity, zone spreading, persistence/storageClass, ingress, and ServiceMonitor options. Start small: two workers do not automatically qualify any particular client count.

## Install and open the UI

```sh
helm lint deploy/helm/mqtitan -f values.local.yaml
helm template mqtitan deploy/helm/mqtitan -n mqtitan -f values.local.yaml
helm upgrade --install mqtitan deploy/helm/mqtitan \
  --namespace mqtitan -f values.local.yaml
kubectl -n mqtitan rollout status deployment/mqtitan
kubectl -n mqtitan rollout status deployment/mqtitan-worker
kubectl -n mqtitan get pods,pvc
kubectl -n mqtitan port-forward service/mqtitan 8080:8080
```

Keep port-forward running. Open http://127.0.0.1:8080 and enter the bearer token in Settings. In Workers, wait for two registered healthy workers. In Tests, create a small test using the broker's cluster-reachable URL, select both workers, and start with 100 clients for 30 seconds. The scenario population is partitioned across selected workers, not duplicated per worker.

For an HTTPS load balancer use `wss://load.example.com:443/mqtt` and configure WebSocket forwarding on the load balancer/HAProxy. Upload required certificates through the UI; profiles stay in the controller's persistent database and resolve to workers for execution. Test ingress HTTPS and broker TLS are separate connections.

Inspect Results and export the report after completion. A local CLI can submit saved scenarios through the port-forward using `--controller http://127.0.0.1:8080` and `--workers` with the registered IDs shown in Workers; supply the same `MQTITAN_TOKEN` locally.

## Operations and shutdown

```sh
kubectl -n mqtitan logs deployment/mqtitan --tail=100
kubectl -n mqtitan logs -l app=mqtitan,component=worker --tail=100
```

Use `helm upgrade ... -f values.local.yaml` after changing worker replicas or resources. Stop active tests before upgrades or scale-down: worker loss stops a run and does not trigger automatic reassignment. The controller uses a Recreate rollout; expect downtime. A disruption budget can block voluntary node eviction.

For remote UI access, configure ingress class, hostname, and an existing TLS Secret rather than exposing an unauthenticated HTTP service. ServiceMonitor requires Prometheus Operator CRDs and matching discovery selectors. See [operations](operations.md#helm) for these options.

Back up the database and adjacent `.key` together. Pod replacement with the same volume preserves results and certificates; it does not resume sockets. Before uninstalling, inspect chart-owned PVCs and the storage class/PV reclaim policy: `helm uninstall mqtitan -n mqtitan` can remove chart-owned storage resources. Do not assume uninstall preserves data. Avoid deleting the namespace until retention and backup requirements are resolved.
