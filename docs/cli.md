# CLI reference

Examples assume a source build and a broker already running. Run `./bin/mqtitan help` for commands and command-specific `--help` for flags.

| Command | Purpose |
| --- | --- |
| `quick [flags]` | Generate and run a simple publishing scenario |
| `validate FILE` | Validate YAML without contacting a broker |
| `plan FILE` | Estimate resource requirements; estimates are not calibrated capacity |
| `run FILE [flags]` | Execute a saved scenario |
| `serve [flags]` | Keep the API and embedded dashboard running |
| `worker [flags]` | Register a distributed load generator |
| `status [TEST_ID] [flags]` | Inspect controller test status |
| `stop TEST_ID [flags]` | Request cancellation |
| `results TEST_ID [flags]` | Retrieve result metadata |
| `export TEST_ID [flags]` | Export metadata and aggregated samples |
| `workers [flags]` | List registered workers |
| `doctor` | Report host limits and recommendations without changing them |
| `version` | Print build version |

## Local and controller-managed execution

```sh
./bin/mqtitan quick --broker mqtt://localhost:1883 \
  --clients 1000 --rate 1 --duration 30s --qos 1 --payload-size 512
./bin/mqtitan quick --broker wss://load.example.com:443/mqtt \
  --mqtt-version 5 --clients 100 --duration 30s
```

Default quick settings: 100 clients, 1 message/sec/client, 30 seconds, 128-byte payload, QoS 0, MQTT 3.1.1, and topic `devices/${clientId}/telemetry`. `--publish-rate` is an alias for `--rate`. Quote topic templates so the shell does not expand variables:

```sh
./bin/mqtitan quick --broker mqtt://localhost:1883 \
  --topic 'devices/${clientId}/telemetry' --save telemetry.yaml
```

Without `--controller`, `quick` and `run` own a local controller, defaulting to `127.0.0.1:8080` and `mqtitan.db`. Its server exits when the command finishes. To keep the UI available, run `serve` separately and submit with `--controller`:

```sh
# Terminal 1
./bin/mqtitan serve --listen 127.0.0.1:8080 --data ./mqtitan.db
# Terminal 2
./bin/mqtitan run examples/10k.yaml --controller http://127.0.0.1:8080
```

Put the scenario path or test ID before flags on `run`, `stop`, `results`, and `export`. A browser disconnect does not cancel a controller-managed test. Interrupting the running CLI requests cancellation.

## Results and distributed workers

```sh
./bin/mqtitan status --controller http://127.0.0.1:8080
./bin/mqtitan results TEST_ID --controller http://127.0.0.1:8080
./bin/mqtitan export TEST_ID --controller http://127.0.0.1:8080 --format json
./bin/mqtitan export TEST_ID --controller http://127.0.0.1:8080 --format csv
./bin/mqtitan stop TEST_ID --controller http://127.0.0.1:8080
./bin/mqtitan workers --controller http://127.0.0.1:8080
```

`results` returns metadata; `export` also includes stored aggregated samples. JSON export uses a `data` envelope; CSV is plain CSV. Reruns are available through the UI and API, not a dedicated CLI command.

```sh
./bin/mqtitan worker --controller http://controller.example.com:8080 \
  --id worker-a --health-listen 127.0.0.1:8081
./bin/mqtitan run examples/10k.yaml \
  --controller http://controller.example.com:8080 --workers worker-a
```

Use unique worker IDs and separate health ports for multiple workers on one host. Supply `MQTITAN_TOKEN` to the controller, worker, and remote CLI when using bearer authentication. Use an authenticated TLS reverse proxy for remote deployment. See [operations](operations.md) for Compose and Helm.

## Credentials and CI

For quick tests, supply `--username` and the `MQTITAN_MQTT_PASSWORD` environment variable. `--save` is rejected when that password is set, preventing plaintext credential export. Avoid secrets in shell history, scenario repositories, URLs, and logs. Certificate profiles are configured through the [UI/API](certificates.md).

Add [thresholds](scenario-spec.md#thresholds-and-latency-interpretation) to a scenario and run it in CI. Exit codes:

| Code | Meaning |
| --- | --- |
| `0` | Successful command or passing run |
| `1` | Configuration, validation, or control-plane error |
| `2` | Failed run, including failed thresholds |
| `130` | Stopped or interrupted run |

`doctor` normally exits successfully even when it reports warnings; it is a diagnostic tool, not an automatic readiness gate. Never interpret a passing threshold without checking achieved client counts, rates, and generator resource usage.
