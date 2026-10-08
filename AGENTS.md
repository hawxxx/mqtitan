# Agent instructions

## Image and releases

- Image: `ghcr.io/hawxxx/mqtitan` (linux/amd64, linux/arm64). Never use the name `kaflux`; that is a different app.
- Do not hand-edit versions or create release tags. Versioning is automated by release-please (`.github/workflows/release-please.yml`): conventional commits on `main` open a release PR that bumps the version, `CHANGELOG.md`, and Helm `Chart.yaml` (`version` and `appVersion`). Merging it tags `vX.Y.Z` and dispatches `.github/workflows/release.yml`.
- Use conventional commit prefixes (`feat:`, `fix:`, `docs:`, `chore:`, `feat!:` for breaking). Pre-1.0, `feat` bumps minor and `fix` bumps patch.
- Tags published: `X.Y.Z`, `X.Y`, `X` (not for 0.x), `main`, `sha-<commit>`, and `latest` (stable `vX.Y.Z` tags only). Pin deployments by digest (`image.digest` in Helm, `MQTITAN_IMAGE` in Compose).
- `release.yml` scans with Trivy first and publishes nothing if fixable HIGH/CRITICAL vulnerabilities exist, then pushes with SBOM, max provenance, and a GitHub artifact attestation.

## Workflow and image rules

- Pin every GitHub Action to a full commit SHA with a `# vX.Y.Z` comment, keep top-level `permissions: {}`, grant per-job minimums, and start each job with `step-security/harden-runner`. Validate workflow changes with `actionlint`.
- Dockerfile base images are pinned by digest (Dependabot updates them). The image must run as non-root UID 10001 and cross-compile arm64 natively without QEMU.
- Go toolchain is 1.26 (`go.mod`, Dockerfile, CI). Keep them in sync.

## CLI

- Remote commands default `--controller` from `MQTITAN_CONTROLLER`; `MQTITAN_TOKEN` supplies the bearer token. The version is stamped at build time via `-X main.version`.

## Checks before committing

`make check` (Go race tests, vet, web tests and build); `helm lint deploy/helm/mqtitan`; `docker build .`.
