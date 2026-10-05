# Contributing

Keep changes focused, distinguish implemented behavior from roadmap goals, and provide evidence for performance/reliability claims.

Install Go 1.25, Node.js 22, npm, and Make. From the repository root:

```sh
make build
make check
```

The build embeds the dashboard. Checks run Go race tests, vet, frontend tests, and the frontend build; see [Makefile](Makefile). Add tests for validation, cancellation, clean shutdown, and failure paths. Benchmark hot-path changes. Avoid per-message logging, unbounded queues, uncontrolled metric labels, and raw-event persistence.

Update the [documentation index](docs/README.md) and relevant references when interfaces change. Keep examples valid and mark commands generating broker traffic. Future capabilities belong in the [roadmap](docs/roadmap.md); prior evidence and limitations are in [verification](docs/verification.md).

Report issues with sanitized reproductions. Never attach databases, encryption keys, passwords, tokens, or private certificates. Arrange a private channel with the maintainer before disclosing exploitable security details publicly.

Contributions use [Apache-2.0](LICENSE); preserve [third-party notices](THIRD_PARTY_NOTICES.md).
