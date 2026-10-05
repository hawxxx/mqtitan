# Troubleshooting

Start with `./bin/mqtitan doctor`, aggregated test errors, and achieved client/message rates. Doctor is read-only and does not tune the host.

| Symptom | Check |
| --- | --- |
| Dashboard disappears after a test | Local quick/run owns a temporary server. Use separate serve plus `--controller`; results persist. |
| Address already in use | Submit to the existing controller or choose a separate port and database. |
| Container cannot reach localhost | Localhost refers to that container. The Compose broker is `mqtt://broker:1883`. |
| HTTPS load balancer connection fails | Use `wss://HOST:443/mqtt` and forward WebSocket upgrades to the broker's WebSocket listener. |
| TLS authority/hostname error | Supply the correct CA and SNI. Disabling verification is not a production fix. |
| mTLS differs behind a load balancer | Locate TLS termination. Client certificates authenticate the immediate TLS peer, not automatically the backend broker. |
| Targets are not reached | Inspect connection pacing, acknowledgements, CPU/memory, descriptors, ports, bandwidth, and broker errors. |
| Worker disappears | Check unique IDs, token, reachability, heartbeat errors, and health-port conflicts. Reassignment is not automatic. |
| Missing cross-host delivery latency | Enable correlation and matching subscribers; synchronize clocks. Invalid samples are excluded. |
| Threshold fails without traffic | Missing measurements fail thresholds rather than producing false passes. |
| Encrypted records unreadable | Restore the matching adjacent `.key`; a replacement key cannot decrypt old data. |
| Certificates remain after pod termination | Profiles persist in the database volume. Pod replacement does not erase persistent storage. |
| No active run after controller restart | Restart interrupts stored active runs; MQTT sockets are not restored. |

Review tuning recommendations before applying them, including host versus container limits and effects on other workloads. Large connection populations may need multiple generator hosts even with low CPU usage.

Issue reports should include version, a sanitized scenario, transport/QoS, topology, worker count, achieved rates, aggregated errors, and doctor output. Remove passwords, tokens, private keys, and sensitive hostnames. See [Contributing](../CONTRIBUTING.md).
