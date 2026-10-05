# Documentation

MQTTitan is an engineering preview. Deployment manifests and large scenarios are starting points for qualification, not proof of production capacity.

| Task | Guide |
| --- | --- |
| Build and run a first test | [Project quick start](../README.md#quick-start) |
| Use terminal commands or CI | [CLI reference](cli.md) |
| Write reusable workloads | [Scenario specification](scenario-spec.md), [examples](../examples/README.md) |
| Change a running test | [Live controls](live-controls.md) |
| Repeat a finished test | [Rerunning tests](reruns.md) |
| Configure TLS or upload certificates | [Certificates](certificates.md) |
| Deploy controllers and workers | [Operations](operations.md) |
| Collect broker-side metrics | [EMQX integration](emqx.md) |
| Automate through HTTP | [REST API](api.md) |
| Diagnose failures | [Troubleshooting](troubleshooting.md) |
| Understand implementation and limits | [Architecture](architecture/overview.md), [roadmap](roadmap.md) |
| Develop and verify changes | [Contributing](../CONTRIBUTING.md), [verification evidence](verification.md) |

The historical design in `superpowers/plans/` records intent. Use the architecture guide and roadmap for the distinction between implemented behavior and future work.
