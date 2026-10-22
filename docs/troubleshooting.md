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

## Connect failure stages

Failed connects are classified once and counted per stage in `connectFailures` (and `emqxload_connect_failures_total{stage}`). `mqtt_connack` failures also record the refusal code in `connackReasons`.

| Stage | Usual cause |
| --- | --- |
| `dns` | The broker name did not resolve or the resolver timed out. Check the name, resolver reachability and search domains. |
| `tcp` | Connection refused or unreachable: wrong port, no listener, firewall, or security group. |
| `tls_handshake` | The peer is not speaking TLS (plaintext port behind an `mqtts://` URL), unsupported TLS version or ciphers, or the connection was closed mid-handshake. |
| `tls_verify_chain` | The server certificate does not chain to the configured CA, is expired, or lacks the server-auth usage. Supply the right CA. |
| `tls_verify_hostname` | Name mismatch or CN-only certificate. Check `serverName`, and see `hostnameVerification` (`cn-fallback` accepts CN-only certificates). |
| `tls_alert` | The server sent a TLS alert, typically because it rejected the client certificate (TLS 1.3 reports this after the handshake completes). |
| `websocket_upgrade` | The HTTP upgrade failed: wrong path, a proxy not forwarding upgrades, or a missing `mqtt` subprotocol. |
| `mqtt_connack` | The broker refused the CONNECT. Codes 4 and 5 (MQTT 3.1.1) or 134 and 135 (MQTT 5) mean bad credentials or not authorized; see `connackReasons`. |
| `timeout` | The connect did not finish within `connectTimeout`: silent drops, overloaded broker or load balancer. |
| `other` | Anything not matched above. |

Review tuning recommendations before applying them, including host versus container limits and effects on other workloads. Large connection populations may need multiple generator hosts even with low CPU usage.

Issue reports should include version, a sanitized scenario, transport/QoS, topology, worker count, achieved rates, aggregated errors, and doctor output. Remove passwords, tokens, private keys, and sensitive hostnames. See [Contributing](../CONTRIBUTING.md).
