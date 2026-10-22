# NLB Balance Testing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let one mqtitan run prove that connections spread evenly across independent EMQX pods and AZs behind cross-zone-off NLBs, that a DNS weighted split is honoured, and that the run can authenticate against brokers whose certificates carry only a CN wildcard, with every connect failure attributed to the stage that failed.

**Architecture:** Six PRs, nine test-first tasks. PR 1 adds hostname verification modes so CN-only broker certificates are verified instead of skipped. PR 2 classifies connect failures by stage. PR 3 adds a resolving dialer with resolver failover plus bounded per-address, per-label and per-DNS-answer counters through worker reports, samples, REST/SSE, `/metrics` and the dashboard. PR 4 adds lifetime churn, validate-time safety and secret checks, and plan/validate warnings for proxy idle timeouts and source-port capacity. PR 5 adds multi-target broker telemetry (EMQX list and Prometheus) polled by the controller, with expected-target and baseline handling, balance statistics and two thresholds. PR 6 adds the example, docs and the compose end-to-end harness.

**Tech Stack:** Go 1.26 standard library (`crypto/x509`, `crypto/tls`, `net.Resolver` with `PreferGo`, `net/netip`, `math/rand/v2`, `regexp`), `golang.org/x/net/dns/dnsmessage` for the test DNS server (already a direct dependency), the existing React dashboard (`web/src/App.tsx`, `web/src/model.ts`), EMQX 5.8.8 and Prometheus in Docker Compose for end-to-end.

**Verified against:** `hawxxx/mqtitan` main `f72159e021ea6be575f90921385edc3568a3ca4a` (merge of #30) and the 0.3.0 release PR #31 head `9cba605bf4e463785be6d4539202de78125461a6` (`chore(main): release 0.3.0`, open, base main). The first clone resolved to `c02b3b4804da2ed5fdfd2be76a7b69ede7e7f7f9`; main moved to `f72159e` during review, so every reference below was re-checked at the PR #31 head, which is the baseline this plan builds on. See "Verification record" at the end.

**Spec:** The requirements in the conversation that produced this plan. Key values are copied into each task.

## PR map

| PR | Tasks | Conventional title |
| --- | --- | --- |
| 1 | Task 1 | `feat: verify CN-only broker certificates with tls.hostnameVerification` |
| 2 | Task 2 | `feat: classify connect failures by stage` |
| 3 | Tasks 3, 4 | `feat: add broker.dns resolver controls and address distribution` |
| 4 | Tasks 5, 6 | `feat: add lifetime churn, safety guardrails and plan warnings` |
| 5 | Tasks 7, 8 | `feat: add per-test broker telemetry and balance thresholds` |
| 6 | Task 9 | `docs: add NLB balance example, scenario docs and compose e2e` |

PR 1 and PR 2 are independent of each other; PR 2 must merge before PR 3 because the resolving dialer keeps the stage-tagged errors PR 2 introduces. PRs 4 and 5 depend on PR 3 only for shared interfaces named in each task. PR 6 depends on all earlier PRs.

## Global Constraints

- Baseline is the 0.3.0 code (PR #31 head). Controller and workers must run the same release (0.3.0 breaking change: registration session and gzip reports); every new report field must travel inside the existing gzip-compressed `api.Report` heartbeat, bounded by `input()` at 2 MiB compressed and 8 MiB decompressed (`internal/api/http.go`).
- No new Go dependencies. Prometheus querying uses `net/http` + `encoding/json` against `/api/v1/query`; DNS uses `net.Resolver{PreferGo: true, Dial: ...}`; tests use `golang.org/x/net/dns/dnsmessage` (`golang.org/x/net v0.60.0` is a direct requirement in `go.mod`). The Prometheus image in the e2e compose file is pinned by digest.
- Credentials are never in YAML: EMQX targets reuse `MQTITAN_EMQX_API_KEY` / `MQTITAN_EMQX_API_SECRET`; Prometheus uses `MQTITAN_PROMETHEUS_TOKEN` (bearer) or `MQTITAN_PROMETHEUS_USERNAME` / `MQTITAN_PROMETHEUS_PASSWORD`. Scenario YAML holds URLs and queries only.
- API submissions keep rejecting `caFile`/`certFile`/`keyFile` (`decodeScenario`, `internal/api/manager.go:122`, added in #30). File-based secret checks apply to standalone CLI and local-controller runs (`decodeLocalScenario`, `Manager.StartLocal`); distributed runs through a remote controller use certificate profiles or inline PEM.
- With `broker.dns`, `broker.addressLabels`, `broker.idleTimeoutHint`, `broker.tls.hostnameVerification`, `clients.lifetime`, `safety` and `telemetry` absent, behaviour and output are unchanged, with three deliberate exceptions: the additive `connectFailures`/`connackReasons` breakdown, the MQTT 5 CONNACK refusal error text (now `CONNACK reason <code>`, see Task 2), and `validate` printing existing `Plan.Warnings` lines before `VALID`. `connection_error_rate` keeps its current definition.
- Hot connect path: no map allocation, no string formatting, no regex per connect. Counter keys are `netip.AddrPort` / `netip.Addr` (comparable, alloc-free); stage counters are a fixed array; label lookup is a precomputed `[]netip.Prefix` scan.
- Every label map is cardinality-bounded: at most 1024 distinct keys per counter; further keys go to the key `other`. No per-client rows are stored.
- Addresses from `net.Resolver.LookupNetIP` and `TCPAddr.AddrPort()` are passed through `Unmap()` before use; IPv4-mapped IPv6 values never reach counters or prefix matching.
- Thresholds with missing data fail with observed `no observations` (same as existing thresholds).
- Conventional commits, one PR per row of the PR map, branched from `main`. No version, `CHANGELOG.md` or `Chart.yaml` edits: release-please owns them. All PRs are additive (`feat:`/`docs:`), no `feat!`. Scenario `apiVersion` stays `mqtitan.io/v1alpha1`.
- Never log key contents or full key paths at info level; warnings name the file by basename. Errors never include certificate PEM, API secrets or response bodies.
- Repo docs, examples, test fixtures and code stay generic: no customer, operator or vendor hostnames, NLB names, account IDs, deployment names or topic trees. Use `example.com`/`example.net`/`.test` names and RFC 5737 documentation ranges. Environment specifics (hostnames, certificates, cluster and network details, secret locations, field checklist) live in a private notes file outside the repository and never in this plan, which is tracked in a public repository.
- Checks before every commit (AGENTS.md): `make check` (Go race tests, vet, web tests and build). Before each PR: `helm lint deploy/helm/mqtitan` and `docker build .`. Update `docs/README.md` and `docs/api.md` when interfaces change (CONTRIBUTING.md).

## New scenario fields (reference for every task)

| Field | Default | Validation error text (substring) | Task |
| --- | --- | --- | --- |
| `broker.tls.hostnameVerification` | `""` = `san` (current behaviour) | `broker.tls.hostnameVerification must be san, cn-fallback or none`; `broker.tls.hostnameVerification cannot be combined with insecureSkipVerify`; non-TLS scheme: existing `TLS settings require mqtts or wss` | 1 |
| `broker.dns.resolveEachDial` | `false` | none | 3 |
| `broker.dns.resolvers` | `[]` (system resolver) | `broker.dns.resolvers[i] must be an IP:port literal`; more than 16 entries: `broker.dns.resolvers allows at most 16 entries` | 3 |
| `broker.dns.pick` | `""` = `first` | `broker.dns.pick must be first or random` | 3 |
| `broker.dns.family` | `""` = `any` | `broker.dns.family must be any, ipv4 or ipv6` | 3 |
| `broker.addressLabels[].cidr` / `.label` | none | `broker.addressLabels[i].cidr is not a valid CIDR`; `broker.addressLabels[i].label is required`; `broker.addressLabels[i].cidr duplicates`; more than 256 entries | 4 |
| `broker.idleTimeoutHint` | `0` (no check) | `broker.idleTimeoutHint must be nonnegative` | 6 |
| `clients.lifetime.min` / `.max` | absent (no churn) | `clients.lifetime.min must be positive`; `clients.lifetime.max must not be less than min` | 5 |
| `safety.forbiddenTopicPrefixes` | `[]` | `workload "<name>" topic ... matches forbidden prefix "<p>"` | 6 |
| `safety.clientIdDenyRegex` | `""` | `safety.clientIdDenyRegex is not a valid regular expression`; `client ID "<id>" matches safety.clientIdDenyRegex` | 6 |
| `safety.strictSecrets` | `false` | `refusing key file <basename>: ... (safety.strictSecrets)` | 6 |
| `telemetry.interval` | `15s` | `telemetry.interval must be at least 5s` | 7 |
| `telemetry.expectedTargets` | `0` (no check) | `telemetry.expectedTargets must be between 0 and 4096` | 7 |
| `telemetry.baseline` | `""` = `none` | `telemetry.baseline must be none or first` | 7 |
| `telemetry.emqx[].name/zone/url` | none | exactly one of `emqx` or `prometheus`: `telemetry must set exactly one of emqx or prometheus`; duplicate names; URL with userinfo: `credentials must not be in YAML`; more than 1024 targets | 7 |
| `telemetry.prometheus.url/query/targetLabel/zoneLabel` | `targetLabel: pod`, `zoneLabel: zone` | empty url/query; URL with userinfo | 7 |
| thresholds `broker_connection_cv`, `zone_connection_skew` | none | percent, may exceed 100 | 8 |

## New metrics (reference)

| Snapshot JSON field | `/metrics` series | Task |
| --- | --- | --- |
| `connectFailures` (map stage to count) | `emqxload_connect_failures_total{stage}` | 2 |
| `connackReasons` (map decimal code to count) | `emqxload_connack_refusals_total{code}` | 2 |
| `remoteAddresses[]` | `emqxload_remote_connects_total{address,label}`, `emqxload_remote_active{address,label}` | 4 |
| `addressLabels[]` | `emqxload_address_label_active{label}` | 4 |
| `dnsAnswers[]` | `emqxload_dns_answers_total{address}` | 4 |
| `dnsErrors[]` | `emqxload_dns_errors_total{resolver}` | 4 |
| `churned`, `churnReconnects` | `emqxload_churned_total`, `emqxload_churn_reconnects_total` | 5 |
| `Test.balance` | `emqxload_broker_connections{target,zone}`, `emqxload_broker_connection_cv`, `emqxload_zone_connection_skew_ratio`, `emqxload_broker_telemetry_complete` | 8 |

Stages for `connectFailures`: `dns`, `tcp`, `tls_handshake`, `tls_verify_chain`, `tls_verify_hostname`, `tls_alert`, `websocket_upgrade`, `mqtt_connack`, `timeout`, `other`. The sum always equals `connectErrors`.

Per-label series on `/metrics` describe the latest test only (`tests[0]`, the same rule `internal/api/prometheus.go` already uses for the e2e quantiles); fleet totals keep summing all tests.

## Review Focus

1. **CN fallback semantics.** A wildcard matches exactly one leftmost label, partial wildcards never match, and a leaf with any DNS SAN never falls back to the CN. Chain verification always runs. Tests in Task 1: `TestMatchCommonNameTable`, `TestSANPresentDisablesCNFallback`, `TestCNFallbackRejectsWrongCAAndExpired`.
2. **Failure classification order.** A connect timeout must be `timeout`, not the stage it happened in; a TLS 1.3 client-certificate rejection that surfaces while reading CONNACK must be `tls_alert`, not `mqtt_connack` or `other`. Tests in Task 2: `TestClassifyTimeoutWins`, `TestClientCertRejectedIsTLSAlert`.
3. **Subscriber wildcards versus forbidden prefixes.** A subscriber on `#`, `+/fleet/#` or `$share/g/devices/#` receives forbidden traffic though its literal prefix does not match. Expect rejection. Test in Task 6: `TestForbiddenPrefixWildcardFilters`.
4. **Recursive resolver caching hides the weighted split.** Docs say so and recommend listing the hosted zone's authoritative name servers as `IP:53`. Resolvers are queried strictly round-robin per lookup; on timeout or SERVFAIL the next resolver in the rotation is tried before the dial fails. Tests in Task 3: `TestResolverRoundRobin`, `TestResolverFailover`.
5. **Mapped addresses.** `LookupNetIP` can return IPv4-mapped IPv6 values, which `netip.Prefix.Contains` will not match against IPv4 prefixes. Tests in Task 3 and 4 assert `Is4()` answers and IPv4 label matches.
6. **Telemetry peak versus ramp-down.** Thresholds use the sample with the highest total connections. Test in Task 8: `TestBalanceThresholdUsesPeakSample`.
7. **Partial telemetry.** A timed-out EMQX target or fewer Prometheus series than `expectedTargets` marks the sample incomplete and balance thresholds fail. Tests in Task 7 and 8: `TestPrometheusFewerThanExpectedTargets`, `TestBalanceThresholdFailsOnIncompleteSample`.
8. **Missing zone labels are not a pass.** If any target lacks a zone, zone statistics are absent and `zone_connection_skew` fails with `no observations`. Unit test `TestZoneStatsRequireEveryZone` and the compose `nozone` case in Task 9.
9. **Key files on filesystems without POSIX modes** (WSL `/mnt/c` and some container mounts report 0777). Expect a warning naming the basename, and `strictSecrets: true` to refuse with a message that says why. Test in Task 6: `TestSecretHygieneWideMode`.

---

### Task 1: Hostname verification modes for CN-only broker certificates (PR 1)

Go 1.26 rejects certificates without SANs during hostname verification (`x509: certificate relies on legacy Common Name field, use SANs instead`), so scenarios against such brokers use `insecureSkipVerify` today. Decision: add `broker.tls.hostnameVerification`. `cn-fallback` performs the full chain verification itself in `tls.Config.VerifyConnection` and then a SAN or CN hostname check with libmosquitto/OpenSSL CN-fallback semantics (`X509_CHECK_FLAG_NO_PARTIAL_WILDCARDS`). The verifier is installed in `NewConnector` after `cfg.ServerName` is defaulted, because the expected name is the `tls.serverName` or URL host and must not depend on what `ConnectionState.ServerName` reports for IP literals.

**Files:**
- Create: `internal/mqtt/verify.go`, `internal/mqtt/verify_test.go`
- Modify: `internal/scenario/scenario.go` (`TLS` struct, `Validate` TLS block), `internal/scenario/extended_test.go`, `internal/mqtt/client.go:76-89` (`NewConnector`), `docs/scenario-spec.md` (TLS paragraph), `docs/certificates.md` ("Security and limits")

**Interfaces:**
- Produces (scenario): `TLS` gains `HostnameVerification string \`yaml:"hostnameVerification,omitempty"\`` with values `""`, `san`, `cn-fallback`, `none`.
- Produces (mqtt):
  ```go
  // ChainError reports that the peer chain did not verify against the trust roots.
  type ChainError struct{ Err error }
  func (e *ChainError) Error() string { return "TLS certificate chain verification failed: " + e.Err.Error() }
  func (e *ChainError) Unwrap() error { return e.Err }

  // HostnameMismatchError reports that neither SAN nor CN matched the expected name.
  type HostnameMismatchError struct{ Name, Mode string }
  func (e *HostnameMismatchError) Error() string {
      return "TLS certificate does not match " + e.Name + " (hostnameVerification " + e.Mode + ")"
  }

  func matchCommonName(pattern, host string) bool
  func verifyPeer(roots *x509.CertPool, mode, name string) func(tls.ConnectionState) error
  ```
  Task 2 classifies `*ChainError` as `tls_verify_chain` and `*HostnameMismatchError` as `tls_verify_hostname`.

- [ ] **Step 1: Write failing validation tests** in `internal/scenario/extended_test.go`, `TestHostnameVerificationValidation`: `cn-fallback` with `mqtts://` passes; `none` with `wss://` passes; `strict` fails with `broker.tls.hostnameVerification must be san, cn-fallback or none`; `cn-fallback` plus `insecureSkipVerify: true` fails with `cannot be combined with insecureSkipVerify`; `cn-fallback` with `mqtt://` fails with `TLS settings require mqtts or wss`.
- [ ] **Step 2: Run** `go test ./internal/scenario -run TestHostnameVerificationValidation` and confirm FAIL (`field hostnameVerification not found`).
- [ ] **Step 3: Implement validation** in `scenario.go`: add the field to `TLS`; in `Validate` add the two checks and include `tls.HostnameVerification != ""` in the existing "TLS settings require mqtts or wss" condition.
- [ ] **Step 4: Write failing matcher test** `TestMatchCommonNameTable` in `verify_test.go`:
  ```go
  func TestMatchCommonNameTable(t *testing.T) {
      cases := []struct{ pattern, host string; want bool }{
          {"*.zone-a.example.net", "broker-01.zone-a.example.net", true},
          {"*.zone-a.example.net", "broker-a1b2.zone-a.example.net", true},
          {"*.zone-a.example.net", "BROKER-01.Zone-A.Example.Net.", true},
          {"*.zone-a.example.net", "other-broker.example.net", false},
          {"*.zone-a.example.net", "a.b.zone-a.example.net", false},
          {"*.zone-a.example.net", "zone-a.example.net", false},
          {"*.zone-a.example.net", ".zone-a.example.net", false},
          {"broker.example.net", "broker.example.net", true},
          {"broker.example.net", "other.example.net", false},
          {"broker*.example.net", "broker-01.example.net", false}, // partial wildcard
          {"*.*.example.net", "a.b.example.net", false},
          {"*.net", "example.net", false},                       // needs two labels after *
          {"*.example.net", "192.0.2.1", false},
          {"", "broker.example.net", false},
      }
      for _, c := range cases {
          if got := matchCommonName(c.pattern, c.host); got != c.want {
              t.Errorf("matchCommonName(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
          }
      }
  }
  ```
- [ ] **Step 5: Write failing handshake tests** in `verify_test.go`. Add helper `newTestPKI(t) (caPEM []byte, issue func(tmpl *x509.Certificate) tls.Certificate)` using `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)` and `x509.CreateCertificate`; leaf templates set `Subject.CommonName` and leave `DNSNames` empty unless stated. Serve with a mochi `listeners.NewTCP` TLS listener (pattern from `TestTLSCAAndServerNameVerification`, `client_test.go:93`) and, for wss, `httptest.NewUnstartedServer` with `TLS` set and `websocket.Handler(... b.EstablishConnection("ws", c))` (pattern from `TestWebsocketDelivery`, `client_test.go:25`). Run each case for versions `3.1.1` and `5`, URL `mqtts://127.0.0.1:<port>` or `wss://127.0.0.1:<port>`, `tls.caPem` = test CA, `tls.serverName: broker-01.zone-a.example.net`:
  - `TestCNOnlyCertificateDefaultSANFails`: CN `*.zone-a.example.net`, mode `""` -> connect error whose text contains `legacy Common Name field`.
  - `TestCNFallbackAcceptsCNOnlyCertificate`: same leaf, `cn-fallback` -> connects over mqtts and wss.
  - `TestCNFallbackRejectsWrongHost`: `serverName: other-broker.example.net` -> `errors.As(err, new(*HostnameMismatchError))`.
  - `TestSANPresentDisablesCNFallback`: leaf CN `*.zone-a.example.net`, `DNSNames: ["other.example.net"]` -> `*HostnameMismatchError`; with `serverName: other.example.net` -> connects.
  - `TestCNFallbackRejectsWrongCAAndExpired`: leaf signed by a second CA -> `*ChainError` wrapping `x509.UnknownAuthorityError`; leaf with `NotAfter` one hour ago -> `*ChainError` wrapping `x509.CertificateInvalidError` with `Reason == x509.Expired`.
  - `TestCNFallbackIgnoresUnrelatedIntermediates`: server sends leaf plus an unrelated self-signed CA in its chain -> connects (path building ignores it).
  - `TestHostnameVerificationNoneChecksChainOnly`: `none` with `serverName: anything.invalid` -> connects; with wrong CA -> `*ChainError`.
  - `TestServerAuthUsageRequired`: leaf with `ExtKeyUsage: [ClientAuth]` only -> `*ChainError` (incompatible usage).
- [ ] **Step 6: Run** `go test ./internal/mqtt -run 'MatchCommonName|CNOnly|CNFallback|SANPresent|HostnameVerificationNone|ServerAuthUsage'` and confirm FAIL.
- [ ] **Step 7: Implement `verify.go`:**
  ```go
  func matchCommonName(pattern, host string) bool {
      pattern = strings.TrimSuffix(strings.ToLower(pattern), ".")
      host = strings.TrimSuffix(strings.ToLower(host), ".")
      if pattern == "" || host == "" || net.ParseIP(host) != nil {
          return false
      }
      if !strings.Contains(pattern, "*") {
          return pattern == host
      }
      rest, ok := strings.CutPrefix(pattern, "*.")
      if !ok || strings.Contains(rest, "*") || strings.Count(rest, ".") < 1 {
          return false // partial, multiple or too-broad wildcards never match
      }
      label, suffix, ok := strings.Cut(host, ".")
      return ok && label != "" && suffix == rest
  }

  func verifyPeer(roots *x509.CertPool, mode, name string) func(tls.ConnectionState) error {
      return func(cs tls.ConnectionState) error {
          if len(cs.PeerCertificates) == 0 {
              return &ChainError{errors.New("server sent no certificate")}
          }
          leaf := cs.PeerCertificates[0]
          inter := x509.NewCertPool()
          for _, c := range cs.PeerCertificates[1:] {
              inter.AddCert(c)
          }
          if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter,
              KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
              return &ChainError{err}
          }
          if mode == "none" {
              return nil
          }
          if len(leaf.DNSNames) > 0 || len(leaf.IPAddresses) > 0 {
              if leaf.VerifyHostname(name) != nil { // SAN present: no CN fallback
                  return &HostnameMismatchError{name, mode}
              }
              return nil
          }
          for _, atv := range leaf.Subject.Names { // every CN attribute, as OpenSSL does
              if atv.Type.Equal(asn1.ObjectIdentifier{2, 5, 4, 3}) {
                  if cn, ok := atv.Value.(string); ok && matchCommonName(cn, name) {
                      return nil
                  }
              }
          }
          return &HostnameMismatchError{name, mode}
      }
  }
  ```
  In `NewConnector`, after `cfg.ServerName` defaulting: for `cn-fallback` and `none`, set `roots := cfg.RootCAs`, falling back to `x509.SystemCertPool()` (fail with `TLS system trust roots unavailable` on error), then `cfg.InsecureSkipVerify = true` and `cfg.VerifyConnection = verifyPeer(roots, mode, cfg.ServerName)`. `san` and `""` leave the config unchanged. `insecureSkipVerify: true` stays a diagnostics-only bypass.
- [ ] **Step 8: Run** `go test -race ./internal/mqtt ./internal/scenario` and confirm PASS, including the unchanged `TestTLSCAAndServerNameVerification`.
- [ ] **Step 9: Docs.** `docs/scenario-spec.md` TLS paragraph: document the field, default `san`, the three modes, that `cn-fallback` verifies the chain with server-auth usage before any name check, the wildcard rules from the test table, that any DNS or IP SAN disables CN fallback, and why the field exists (Go rejects CN-only certificates; reissuing with a SAN is the preferred long-term fix). `docs/certificates.md`: replace "Do not use certificate uploads to bypass server-name validation" with a sentence saying `cn-fallback` keeps chain and name verification, `none` keeps chain verification only, and `insecureSkipVerify` disables both.
- [ ] **Step 10: Run** `make check` and confirm PASS.
- [ ] **Step 11: Commit** `feat: verify CN-only broker certificates with tls.hostnameVerification`

### Task 2: Connect failure classification (PR 2)

Decision: tag errors at the point where the stage is known (`dial` and `connect3`/`connect5`) with a `*StageError`, and classify once in the engine. Classification checks timeouts first, then stage tags, then MQTT CONNACK refusals, then remote TLS alerts recorded by a connection wrapper, then generic `net` errors. A wrapper is needed because a TLS 1.3 server rejects a client certificate after the client's handshake completes, so the alert surfaces on the first read inside the MQTT library, and library error wrapping differs between paho v3 and paho.golang. `websocket_upgrade` and `other` are added to the requested stages so the per-stage counts always sum to `connectErrors`.

Two library facts verified for this design: paho.mqtt.golang v1.5.1 returns `packets.ConnErrors[rc]` sentinels for refusals and wraps network errors with `%w` (`client.go:443-445`); paho.golang v0.23.0 `Client.Connect` returns a non-nil `*Connack` together with a non-nil error when the reason code is >= 0x80 (`paho/client.go` around line 311), so the existing `CONNACK reason %d` branch in `connect5` (`internal/mqtt/client.go:300-311`, check at line 308) is unreachable for refusals and must inspect `ack` before `err`.

**Files:**
- Create: `internal/mqtt/classify.go`, `internal/mqtt/classify_test.go`
- Modify: `internal/mqtt/client.go:111-161` (`dial`), `:191-237` (`connect3`), `:287-313` (`connect5`); `internal/metrics/metrics.go` (`Counters`, `Snapshot`, `Snapshot()`, `MergeSnapshots`); `internal/engine/engine.go:202-364` (`runSession` connect error branch); `internal/api/prometheus.go`; `internal/api/http.go` export CSV header; `web/src/model.ts` (`Snapshot`); `web/src/App.tsx` (TestDetail counters near the `["Reconnects", ...]` row at line 1493, and the Errors page at line 1980); `docs/api.md`, `docs/troubleshooting.md`

**Interfaces:**
- Produces (mqtt):
  ```go
  const (
      StageDNS = "dns"; StageTCP = "tcp"; StageTLSHandshake = "tls_handshake"
      StageTLSVerifyChain = "tls_verify_chain"; StageTLSVerifyHostname = "tls_verify_hostname"
      StageTLSAlert = "tls_alert"; StageWebsocket = "websocket_upgrade"
      StageConnack = "mqtt_connack"; StageTimeout = "timeout"; StageOther = "other"
  )
  type StageError struct{ Stage string; Err error }
  type ConnackError struct{ Code byte }      // Error(): "CONNACK reason <code>"
  type Failure struct{ Stage string; Code int } // Code is -1 unless Stage == StageConnack
  func Classify(err error) Failure
  ```
- Produces (metrics):
  ```go
  func (c *Counters) RecordConnectFailure(stage string, code int)
  // Snapshot gains:
  //   ConnectFailures map[string]uint64 `json:"connectFailures,omitempty"`
  //   ConnackReasons  map[string]uint64 `json:"connackReasons,omitempty"` // key = decimal code
  ```
  Counters use `[len(connectStages)]atomic.Uint64` and `[256]atomic.Uint64`, following the `errorCategories` array pattern from 0.3.0 (`metrics.go`).

- [ ] **Step 1: Write failing unit tests** in `classify_test.go` (table, no network): `context.DeadlineExceeded` wrapped in a `*StageError{tcp}` -> `timeout` (`TestClassifyTimeoutWins`); `*StageError{dns, &net.DNSError{IsTimeout: true}}` -> `dns` (`TestClassifyDNSTimeoutIsDNS`); `*net.OpError{Op:"dial", Err: syscall.ECONNREFUSED}` -> `tcp`; `&net.DNSError{IsNotFound: true}` -> `dns`; `*StageError{tls_handshake, *ChainError}` -> `tls_verify_chain`; `*StageError{tls_handshake, x509.HostnameError{}}` and `*HostnameMismatchError` -> `tls_verify_hostname`; `*StageError{tls_handshake, &net.OpError{Op:"remote error"}}` -> `tls_alert`; `*ConnackError{135}` -> `{mqtt_connack, 135}`; `fmt.Errorf("x: %w", packets.ErrorRefusedNotAuthorised)` (paho v3 sentinel) -> `{mqtt_connack, 5}`; `errors.New("boom")` -> `other`.
- [ ] **Step 2: Write failing integration tests** in `classify_test.go`, both MQTT versions:
  - `TestConnectFailureTCPRefused`: `mqtt://127.0.0.1:<closed port>` -> `tcp`.
  - `TestConnectFailureDNS`: `mqtt://name.invalid:1883` -> `dns`.
  - `TestConnectFailureTimeout`: a `net.Listen` that accepts and never answers, `connectTimeout: 200ms` -> `timeout`.
  - `TestConnectFailureTLSHandshake`: plaintext listener behind an `mqtts://` URL -> `tls_handshake`.
  - `TestConnectFailureTLSVerify`: untrusted server cert -> `tls_verify_chain`; trusted cert with wrong `serverName` -> `tls_verify_hostname`.
  - `TestClientCertRejectedIsTLSAlert`: mochi TLS listener with `ClientAuth: tls.RequireAndVerifyClientCert`, client without certificate, run once with server `MaxVersion: tls.VersionTLS12` and once with TLS 1.3 -> `tls_alert` both times.
  - `TestConnackRefusalCode`: mochi broker with a hook whose `OnConnectAuthenticate` returns false -> `mqtt_connack` with code 5 (v3) and 135 (v5).
  - `TestWebsocketUpgradeFailure`: `ws://` URL to an `httptest` server returning 404 -> `websocket_upgrade`.
- [ ] **Step 3: Run** `go test ./internal/mqtt -run 'Classify|ConnectFailure|ClientCertRejected|ConnackRefusal|WebsocketUpgrade'` and confirm FAIL.
- [ ] **Step 4: Implement.** In `dial`: wrap `net.Dialer` errors in `&StageError{StageTCP, err}` unless `*net.DNSError` (then `StageDNS`); replace `tls.Dialer` with TCP dial plus `tls.Client(conn, cfg).HandshakeContext(ctx)` wrapped as `StageTLSHandshake`; wrap `websocket.NewClient` errors as `StageWebsocket`; wrap the returned conn in `alertConn` that stores the first `Read` error in an `atomic.Pointer[error]`. In `connect5`: check `ack != nil && ack.ReasonCode >= 0x80` before `err` and return `&ConnackError{ack.ReasonCode}`. In both connect functions, when the CONNECT exchange fails, return the `alertConn` read error instead when it is a remote alert (`errors.As(err, &op) && op.Op == "remote error"`). `Classify` order: a `*StageError` with stage `dns` -> `dns` (a resolver that timed out is a DNS failure, not a connect timeout); then timeout (`errors.Is(err, context.DeadlineExceeded)` or `net.Error.Timeout()`), `*ChainError`/`x509.UnknownAuthorityError`/`x509.CertificateInvalidError` -> chain, `*HostnameMismatchError`/`x509.HostnameError` -> hostname, remote alert -> `tls_alert`, `*ConnackError` or a `packets.ConnErrors` sentinel (index by code) -> `mqtt_connack`, then the outermost `*StageError` stage, then `*net.DNSError` -> `dns`, `*net.OpError` with `Op == "dial"` -> `tcp`, else `other`.
- [ ] **Step 5: Metrics and engine.** Add `RecordConnectFailure`; `Snapshot()` renders non-zero entries; `MergeSnapshots` sums both maps. In `runSession` connect error branch (after the `ctx.Err()` cancellation check, which stays), add `f := transport.Classify(err); e.Metrics.RecordConnectFailure(f.Stage, f.Code)` next to the existing `ConnectErrors.Add(1)` and `RecordError("connect")`. Tests in `internal/metrics/metrics_test.go`: `TestConnectFailuresMergeAndSumToErrors`, `TestRecordConnectFailureNoAlloc` (`testing.AllocsPerRun == 0`). Engine test `TestEngineCountsConnectFailureStage` against a closed port: `connectFailures["tcp"] == connectErrors`.
- [ ] **Step 6: Expose.** `/metrics`: `emqxload_connect_failures_total{stage="..."}` for every stage (zero included, stable order) and `emqxload_connack_refusals_total{code="..."}` for non-zero codes, summed across tests like the existing counters. Test `TestPrometheusConnectFailureSeries` in `internal/api/api_test.go`. SSE/REST need no new route; add `TestStreamIncludesConnectFailures`. CSV export appends a `connect_failures` column formatted `stage=n;stage=n`. `web/src/model.ts` adds `connectFailures?: Record<string, number>` and `connackReasons?: Record<string, number>`; `App.tsx` TestDetail shows a "Connect failures by stage" table under the counters when any entry is non-zero, and the Errors page lists the top stage per test. Vitest `App.test.tsx`: renders `tls_verify_hostname 3`.
- [ ] **Step 7: Docs.** `docs/api.md` documents both snapshot fields; `docs/troubleshooting.md` gains a table mapping each stage to its usual cause (for example `tls_alert`: server rejected the client certificate; `tls_verify_hostname`: name mismatch or CN-only certificate, see `hostnameVerification`).
- [ ] **Step 8: Run** `make check` and confirm PASS.
- [ ] **Step 9: Commit** `feat: classify connect failures by stage`

### Task 3: Resolving dialer with resolver failover and dial hooks (PR 3)

**Files:**
- Create: `internal/mqtt/dialer.go`, `internal/mqtt/dialer_test.go`
- Modify: `internal/scenario/scenario.go` (`Broker` struct, `Validate`), `internal/scenario/extended_test.go`, `internal/mqtt/client.go` (`Connector`, `NewConnector`, `dial`, `client3`/`client5`)

**Interfaces:**
- Consumes: `StageError`, `alertConn` (Task 2); `verifyPeer` installation in `NewConnector` (Task 1).
- Produces (scenario):
  ```go
  type DNS struct {
      ResolveEachDial bool     `yaml:"resolveEachDial,omitempty"`
      Resolvers       []string `yaml:"resolvers,omitempty"` // IP:port literals
      Pick            string   `yaml:"pick,omitempty"`      // "", "first", "random"
      Family          string   `yaml:"family,omitempty"`    // "", "any", "ipv4", "ipv6"
  }
  // Broker gains: DNS *DNS `yaml:"dns,omitempty"`
  ```
- Produces (mqtt):
  ```go
  type Hooks struct {
      OnResolve      func(answers []netip.Addr)      // once per successful lookup, every answer
      OnResolveError func(resolver netip.AddrPort)    // per failed resolver attempt
      OnConnect      func(remote netip.AddrPort)      // after TCP connect succeeds
  }
  func NewConnectorWithHooks(b scenario.Broker, h Hooks) (*Connector, error)
  func ResolveBroker(ctx context.Context, b scenario.Broker) ([]netip.Addr, error) // used by plan (Task 6)
  // Client interface gains: RemoteAddr() netip.AddrPort
  ```

- [ ] **Step 1: Write failing validation tests** `TestDNSValidation` in `extended_test.go`: `pick: "round"` -> `broker.dns.pick`; `family: "ip4"` -> `broker.dns.family`; `resolvers: ["dns.google:53"]` -> `IP:port`; `resolvers: ["192.0.2.53"]` (no port) -> `IP:port`; 17 resolvers -> `at most 16`; `["192.0.2.53:53","[2001:db8::53]:53"]` -> OK; `dns` absent -> OK.
- [ ] **Step 2: Run** `go test ./internal/scenario -run TestDNSValidation` and confirm FAIL.
- [ ] **Step 3: Add `DNS` and validation**; parse resolvers with `netip.ParseAddrPort`.
- [ ] **Step 4: Write failing dialer tests** in `dialer_test.go` with a fake DNS server (`net.ListenPacket("udp", "127.0.0.1:0")`, `dnsmessage.Parser`/`Builder`) that records `(question type, server)` and can be told to answer, drop (timeout) or return `RCodeServerFailure`:
  - `TestResolverRoundRobin`: two servers, `family: ipv4` (one A query per lookup; with `any` Go sends A and AAAA), 4 lookups -> each server receives exactly 2 A queries, alternating.
  - `TestResolverFailover`: server 1 returns SERVFAIL, server 2 answers -> the dial succeeds, `OnResolveError` called once with server 1; server 1 drops packets -> same result within `connectTimeout`; both return SERVFAIL -> dial fails, `Classify` returns `dns`; both drop packets -> dial fails, `Classify` returns `dns` (the resolving dialer tags every lookup error with `StageDNS`); NXDOMAIN from server 1 -> no query sent to server 2.
  - `TestResolveEachDialNoCache`: `resolveEachDial: true`, 3 dials -> 3 lookups; `false` -> 1 lookup for 3 dials.
  - `TestPickFirstOrder` / `TestPickRandomCoversAll`: answers `127.0.0.2,127.0.0.3` with listeners on both; `first` -> all 20 dials to `.2`; `random` (seeded) -> both seen.
  - `TestFamilyFilter`: answers include `::1` and `127.0.0.1`; `ipv4` -> `OnResolve` gets only addresses with `Is4()` true (no IPv4-mapped values); `ipv6` -> only IPv6.
  - `TestDialFallsBackToNextAddress`: first picked address refuses -> second used; `OnConnect` called once with the second.
  - `TestServerNameKeptForResolvedIP`: `mqtts://broker.test:<port>` resolved to `127.0.0.1`, TLS listener with a SAN for `broker.test` -> handshake succeeds and `ServerName == "broker.test"`; with `tls.serverName: other.test` -> `other.test`; with `hostnameVerification: cn-fallback` and a CN-only cert -> succeeds.
  - `TestLookupUsesRootedName`: the fake server records question names; host `broker.test` -> the only question is `broker.test.`, even with a test `resolv.conf`-style search list in effect (assert no question name has an extra suffix).
  - `TestNoDNSBlockUsesSystemDialer`: `dns` nil -> no fake server queries, `OnResolve` never called, `OnConnect` still called.
- [ ] **Step 5: Run** `go test ./internal/mqtt -run 'Resolver|ResolveEach|Pick|Family|FallsBack|ServerName|NoDNS'` and confirm FAIL.
- [ ] **Step 6: Implement `dialer.go`**: one `*net.Resolver` per configured resolver (`PreferGo: true`, `Dial` fixed to that `IP:port`), built once per `Connector`. With a `dns` block but `resolvers: []`, use a single `&net.Resolver{PreferGo: true}` (system servers from `/etc/resolv.conf`, no cache) and still look up the rooted name. Each lookup starts at `resolvers[(next.Add(1)-1) % uint32(n)]` (strict round-robin; `next` is an `atomic.Uint32`), uses `LookupNetIP(ctx2s, network, rooted)` where `rooted` is `host` with a trailing `.` appended when the host is not an IP literal and does not already end in `.`, so `/etc/resolv.conf` `search`/`ndots` (for example `ndots:5` in Kubernetes pods) never sends suffixed names to the configured resolvers, with a per-attempt context of `min(2s, remaining)` and, on timeout, SERVFAIL (`*net.DNSError` with `IsTimeout` or `IsTemporary`) or any non-NXDOMAIN error, calls `OnResolveError` and tries the next resolver; `IsNotFound` fails immediately. Answers pass through `Unmap()` and the family filter, then `OnResolve`. With `ResolveEachDial == false` the first successful answer set is cached on the dialer. `first` keeps resolver order as returned by Go (Go applies RFC 6724 ordering; IPv4 answers keep resolver order, mixed families put the preferred family first); `random` uses a seeded `rand.New(rand.NewPCG(seed, 0))` behind a mutex. Addresses are tried in order with `net.Dialer` until one connects; then `OnConnect(unmapped remote AddrPort)`. With `DNS == nil` dial `host:port` exactly as today and still call `OnConnect`. Errors keep the Task 2 `StageError` tags. `client3`/`client5` store the remote `netip.AddrPort` for `RemoteAddr()`. `NewConnector(b)` becomes `NewConnectorWithHooks(b, Hooks{})`. `ResolveBroker` runs one lookup with these settings for planning.
- [ ] **Step 7: Run** `go test -race ./internal/mqtt ./internal/scenario ./internal/engine` and confirm PASS. Engine tests use in-process mochi brokers (`testBroker`, `engine_test.go:24`); no fake `Client` implementations exist, so only `client3`/`client5` gain `RemoteAddr()`.
- [ ] **Step 8: Commit** `feat: add broker.dns resolver controls and dial hooks`

### Task 4: Bounded address and DNS counters through results, REST/SSE, /metrics and dashboard (PR 3)

**Files:**
- Create: `internal/metrics/labels.go`, `internal/metrics/labels_test.go`, `web/src/AddressDistribution.tsx`, `web/src/AddressDistribution.test.tsx`
- Modify: `internal/metrics/metrics.go` (`Snapshot`, `Snapshot()`, `MergeSnapshots`), `internal/scenario/scenario.go` (`Broker.AddressLabels`), `internal/engine/engine.go:32-126` (`Engine.Run` connector construction) and `runSession` defer, `internal/api/prometheus.go`, `internal/api/api_test.go`, `internal/api/workers_test.go`, `web/src/model.ts`, `web/src/App.tsx` (mount beside `<LatencyPanel>` at line 1484), `internal/engine/engine_test.go`

**Interfaces:**
- Consumes: `mqtt.Hooks`, `Client.RemoteAddr()` (Task 3).
- Produces:
  ```go
  // scenario
  type AddressLabel struct { CIDR string `yaml:"cidr"`; Label string `yaml:"label"` }
  // Broker gains: AddressLabels []AddressLabel `yaml:"addressLabels,omitempty"`

  // metrics
  type LabelCount struct { Key string `json:"key"`; Label string `json:"label,omitempty"`; Connects uint64 `json:"connects"`; Active int64 `json:"active"` }
  func (c *Counters) SetAddressLabels(prefixes []netip.Prefix, labels []string)
  func (c *Counters) RecordRemote(ap netip.AddrPort)      // Connects++, Active++
  func (c *Counters) ReleaseRemote(ap netip.AddrPort)     // Active--
  func (c *Counters) RecordDNSAnswers(answers []netip.Addr)
  func (c *Counters) RecordDNSError(resolver netip.AddrPort)
  // Snapshot gains:
  //   RemoteAddresses []LabelCount `json:"remoteAddresses,omitempty"` // key "IP:port"
  //   AddressLabels   []LabelCount `json:"addressLabels,omitempty"`   // key = label, "unlabelled" for no match
  //   DNSAnswers      []LabelCount `json:"dnsAnswers,omitempty"`      // key "IP", Connects = times returned
  //   DNSErrors       []LabelCount `json:"dnsErrors,omitempty"`       // key "IP:port", Connects = failed attempts
  ```

- [ ] **Step 1: Write failing tests** in `labels_test.go`:
  - `TestRecordRemoteCountsAndActive`: 3x `RecordRemote(a)`, 1x `RecordRemote(b)`, 1x `ReleaseRemote(a)` -> `RemoteAddresses` sorted by key: `{a, 3, 2}`, `{b, 1, 1}`.
  - `TestAddressLabelLongestPrefix`: `198.51.100.0/24 -> wide`, `198.51.100.0/28 -> narrow` -> `198.51.100.3` under `narrow`; `192.0.2.1` under `unlabelled`; an IPv4-mapped `::ffff:198.51.100.3` input is unmapped and counted under `narrow`.
  - `TestLabelCardinalityCap`: 1100 distinct addresses -> 1024 keys plus `other` with Connects 76.
  - `TestRecordRemoteNoAlloc`: after the first record of a key, `testing.AllocsPerRun(1000, func(){ c.RecordRemote(a); c.ReleaseRemote(a) }) == 0`.
  - `TestMergeSnapshotsLabels`: overlapping keys from two workers -> summed per key, stable sort, cap kept.
  - `TestDNSAnswersAndErrorsCounted`: answers `[x,y]` twice and `[x]` once -> x:3, y:2; two errors on resolver r -> r:2.
- [ ] **Step 2: Run** `go test ./internal/metrics -run 'Remote|AddressLabel|Cardinality|MergeSnapshotsLabels|DNSAnswers'` and confirm FAIL.
- [ ] **Step 3: Implement `labels.go`**: `labelSet` with a `sync.Map` from comparable key to `*labelEntry{connects atomic.Uint64; active atomic.Int64}`, an `atomic.Int32` size, and an `other` entry. Prefixes sorted by `Bits()` descending. `Snapshot()` renders keys with `String()` and sorts. `MergeSnapshots` sums by `Key` and applies the cap.
- [ ] **Step 4: Validate `addressLabels`**: `TestAddressLabelsValidation`: `198.51.100.0/33` -> `broker.addressLabels[0].cidr is not a valid CIDR`; empty label -> error; duplicate CIDR -> error; 257 entries -> error.
- [ ] **Step 5: Wire the engine**: in `Engine.Run` build the connector with `NewConnectorWithHooks(b, Hooks{OnResolve: e.Metrics.RecordDNSAnswers, OnResolveError: e.Metrics.RecordDNSError, OnConnect: e.Metrics.RecordRemote})` and call `SetAddressLabels`; in `runSession`'s existing defer call `e.Metrics.ReleaseRemote(c.RemoteAddr())`. Test `TestEngineRecordsRemoteAddress` with a mochi broker: after run end the snapshot has Connects 1 and Active 0 for the broker address.
- [ ] **Step 6: Report size**: `TestHeartbeatWithFullLabelSetsFitsLimit` in `internal/api/workers_test.go` builds a report with all four lists at 1024 entries plus a populated distribution, gzips it like `internal/worker/worker.go` does, and asserts the compressed body is under 2 MiB and the decompressed body under 8 MiB.
- [ ] **Step 7: Prometheus output** in `internal/api/prometheus.go` (latest test only): `emqxload_remote_connects_total{address,label}`, `emqxload_remote_active{address,label}`, `emqxload_address_label_active{label}`, `emqxload_dns_answers_total{address}`, `emqxload_dns_errors_total{resolver}`. Test `TestPrometheusRemoteAddressSeries`.
- [ ] **Step 8: REST/SSE** need no new routes; `Snapshot` already flows through `Test.Snapshot`, `Sample.Snapshot` and `metrics` SSE events. Add `TestStreamIncludesRemoteAddresses`. Samples give the per-label time series that shows each ramp step.
- [ ] **Step 9: Dashboard**: extend `web/src/model.ts` `Snapshot` with the four optional arrays. `AddressDistribution.tsx` renders tables by label and by address (key, label, active, connects, share of active %), a DNS errors table when non-empty, and a stacked bar of active-by-label per sample. Hidden when all arrays are empty. Tests: renders `new-nlb-1a` row with `50.0%` for 1 of 2 active; renders nothing for empty input.
- [ ] **Step 10: Run** `make check` and confirm PASS.
- [ ] **Step 11: Commit** `feat: report remote address, label and DNS answer distribution`

### Task 5: Lifetime churn (PR 4)

**Files:**
- Modify: `internal/scenario/scenario.go` (`Clients`, `Plan`), `internal/mqtt/client.go` (`Client` interface, `client3`, `client5`), `internal/engine/engine.go:137-192` (`runClient`), `:202-364` (`runSession`), `internal/metrics/metrics.go`, `internal/api/prometheus.go`, `web/src/model.ts`, `web/src/App.tsx` (counter row at line 1493), `cmd/mqtitan/main.go:160` (`printPlan`), tests in `internal/engine/engine_test.go`, `internal/mqtt/client_test.go`, `internal/scenario/scenario_test.go`

**Interfaces:**
- Produces:
  ```go
  type Lifetime struct { Min Duration `yaml:"min"`; Max Duration `yaml:"max"` }
  // Clients gains: Lifetime *Lifetime `yaml:"lifetime,omitempty"`
  // Counters/Snapshot gain: Churned (sessions ended by lifetime), ChurnReconnects (sessions started after a churn)
  // Client interface gains: Disconnect() // sends MQTT DISCONNECT, then closes like Close()
  type sessionKind uint8 // first, reconnect, churn
  // runSession(ctx, seq, kind sessionKind, g) (up time.Duration, churned bool)
  // Plan gains: ChurnConnectsPerSecond float64
  ```

- [ ] **Step 1: Failing validation test** `TestLifetimeValidation`: `min: 0s` -> `clients.lifetime.min must be positive`; `max < min` -> `clients.lifetime.max must not be less than min`; `min == max` -> OK.
- [ ] **Step 2: Failing client test** `TestDisconnectSendsMQTTDisconnect` (both versions, mochi broker with an `OnDisconnect` hook capturing the error): `Disconnect()` -> the broker sees a clean disconnect (nil error); `Close()` keeps its current behaviour (socket close only; `client3.Close` and `client5.Close` do not send DISCONNECT today).
- [ ] **Step 3: Failing engine tests** using mochi brokers and short durations:
  - `TestLifetimeChurnReconnects`: lifetime `{50ms, 50ms}`, 1 client, run 230ms, `reconnect.enabled: false` -> `Churned >= 3`, `ChurnReconnects` within 1 of `Churned`, `Reconnects == 0`, `Errors["disconnect"] == 0`.
  - `TestLifetimeDoesNotConsumeRetryBudget`: `reconnect.maxAttempts: 1`, lifetime 20ms, run 200ms -> still connected at the end.
  - `TestLifetimeSpread`: 200 clients, `{1s, 2s}`, fixed seed -> all `lifetimeFor` values in `[1s,2s]` and not all equal.
  - `TestFailureAfterChurnCountsAsReconnect`: a churned session followed by a broker-dropped session -> the next connect increments `Reconnects`, not `ChurnReconnects`.
- [ ] **Step 4: Run** `go test -race ./internal/engine ./internal/mqtt -run 'Lifetime|Disconnect|FailureAfterChurn'` and confirm FAIL.
- [ ] **Step 5: Implement**: `client3.Disconnect()` calls `c.c.Disconnect(250)` then `Close()`; `client5.Disconnect()` calls `c.c.Disconnect(&paho.Disconnect{ReasonCode: 0})` then `Close()`. `lifetimeFor(seq, n)` draws uniformly from `[min,max]` with `rand.New(rand.NewPCG(seed, uint64(seq)<<32|uint64(n)))`. `runSession` adds a `time.Timer` case to its wait selects (connection/subscriber wait and the publish loop); on fire it sets `churned`, calls `c.Disconnect()` and sets `failed = true` so the defer does not record `disconnect`. `runClient` keeps 0.3.0's `everConnected` rule for ordinary sessions and passes `kind`: after a churned session it skips the backoff and retry accounting and the `!r.Enabled` return, and the next session uses `churn` so its connect counts `ChurnReconnects` instead of `Reconnects`.
- [ ] **Step 6: Expose** `emqxload_churned_total`, `emqxload_churn_reconnects_total`; merge in `MergeSnapshots`; dashboard row "Churn" beside "Reconnects"; `model.ts` fields `churned?`, `churnReconnects?`.
- [ ] **Step 7: Plan output**: convert the positional `Plan{...}` literal at `scenario.go:306` to a keyed literal, add `ChurnConnectsPerSecond` = peak clients / mean lifetime; `printPlan` prints `Lifetime churn: 5m0s to 15m0s (~16.7 connects/s at peak)`. Test in `scenario_test.go`: 10000 clients, `{5m,15m}` -> ~16.67.
- [ ] **Step 8: Run** `make check` and confirm PASS.
- [ ] **Step 9: Commit** `feat: add clients.lifetime connection churn`

### Task 6: Safety guardrails, secret hygiene and plan/validate warnings (PR 4)

Decision: warnings live in `Plan.Warnings` (existing field) so the CLI and `/api/v1/plan` show them without new plumbing. `Plan()` stays offline; a new `PlanWith(PlanOptions)` takes the worker count and an optional resolved destination count, which the CLI supplies with `--workers` and `--resolve`. `validate` prints the same warnings before `VALID`. The idle-timeout rule covers any workload that sends nothing for at least the hint (connection, subscriber, or a publisher slower than one message per hint), because all three idle out the same way; connection-only workloads are the common case.

**Files:**
- Create: `internal/scenario/safety.go`, `internal/scenario/safety_test.go`, `internal/mqtt/secrets.go`, `internal/mqtt/secrets_test.go`
- Modify: `internal/scenario/scenario.go` (`Scenario.Safety`, `Broker.IdleTimeoutHint`, `Validate`, `Plan`, new `PlanWith`), `internal/mqtt/client.go:30` (`TLSConfig`), `cmd/mqtitan/main.go:66-82` (validate/plan) and `:160` (`printPlan`), `cmd/mqtitan/cli_test.go`, `internal/api/http.go:223-245` (plan endpoint body gains `workers`), `docs/cli.md`, `docs/api.md`

**Interfaces:**
- Produces:
  ```go
  type Safety struct {
      ForbiddenTopicPrefixes []string `yaml:"forbiddenTopicPrefixes,omitempty"`
      ClientIDDenyRegex      string   `yaml:"clientIdDenyRegex,omitempty"`
      StrictSecrets          bool     `yaml:"strictSecrets,omitempty"`
  }
  // Scenario gains: Safety Safety `yaml:"safety,omitempty"`
  // Broker gains:   IdleTimeoutHint Duration `yaml:"idleTimeoutHint,omitempty"`
  func (s Scenario) CheckRendered(worker string, from, to int) error
  func filterOverlapsPrefix(filter, prefix string) bool
  type PlanOptions struct { Workers int; Destinations int } // zero values: 1 worker, destinations derived
  func (s Scenario) PlanWith(o PlanOptions) Plan // Plan() == PlanWith(PlanOptions{})
  // mqtt
  type SecretFinding struct { File string /* basename */; Problem string }
  func CheckSecretFiles(t scenario.TLS) []SecretFinding
  func TLSConfig(s scenario.TLS, strict bool) (*tls.Config, error)
  ```

- [ ] **Step 1: Failing safety tests** in `safety_test.go` (generic fixtures):
  - `TestForbiddenPrefixPublisher`: prefixes `["devices/"]`, publisher topic `devices/fleet/${clientId}` -> error naming the workload and prefix; `loadtest/${clientId}` -> OK.
  - `TestForbiddenPrefixHashSuffixNormalised`: `devices/#` behaves like `devices/`.
  - `TestForbiddenPrefixWildcardFilters`: prefixes `["devices/fleet/"]`; subscriber `#`, `+/fleet/x`, `devices/+/#`, `$share/g/devices/fleet/a` -> rejected; `ops/#`, `devices/other/#` -> OK. Table-test `filterOverlapsPrefix` directly.
  - `TestForbiddenPrefixRenderedTopic`: topic `${clientId}/x` with idTemplate `devices/fleet/${sequence}` -> rejected.
  - `TestClientIDDenyRegex`: regex `^[0-9a-fA-F]{12}$|^[A-Z0-9]{10}$`, idTemplate `${sequence:012}` -> rejected with the first offending ID; `mqtitan-${sequence:08}` -> OK.
  - `TestClientIDDenyRegexInvalid`: `"(["` -> `safety.clientIdDenyRegex is not a valid regular expression`.
  - `TestCheckRenderedUsesWorker`: idTemplate `${worker}-${sequence}` with `clients.worker: abc` and regex `^abc-` -> rejected; idTemplate `${worker}${sequence:012}` without `clients.worker` and regex `^[0-9a-fA-F]{12}$` -> rejected (the engine renders `${worker}` as empty, giving `000000000001`). `TestCheckRenderedSkippedWithoutRules`: 1,000,000 clients and no safety rules -> `Parse` does not render IDs (assert via a package-level render counter used only in tests).
  - `TestRenderCheckBound`: `clients.count: 1000000` validates in under 2s (skipped with `-short`).
- [ ] **Step 2: Failing warning tests** in `scenario_test.go`:
  - `TestIdleTimeoutHintWarning`: `idleTimeoutHint: 100s`, connection workload, `keepAlive: 180s` -> one warning containing `keepAlive 3m0s >= broker.idleTimeoutHint 1m40s` and `keepAlive <= 50s`; `keepAlive: 60s` -> none; `keepAlive: 0` uses the effective 30s -> none; subscriber workload with `keepAlive: 120s` -> warning; publisher at `ratePerClient: 0.005` (200s period) with `keepAlive: 120s` -> warning; publisher at 1/s -> none; hint absent -> none; `idleTimeoutHint: -1s` -> validation error.
  - `TestSourcePortWarning`: 60000 clients, 1 worker, no labels -> warning `60000 clients per worker exceed ~25000 source ports x 1 destination address`; with 3 `addressLabels` -> none (75000 budget); `PlanWith({Workers: 2})` and 120000 clients, 2 destinations -> none; `PlanWith({Destinations: 1})` overrides labels.
- [ ] **Step 3: Run** `go test ./internal/scenario -run 'Forbidden|ClientIDDeny|CheckRendered|RenderCheck|IdleTimeout|SourcePort'` and confirm FAIL.
- [ ] **Step 4: Implement `safety.go`** and warnings: normalise prefixes (strip trailing `#`, ensure trailing `/`); `filterOverlapsPrefix` strips `$share/<group>/`, then walks levels (`#` overlaps, `+` matches any level, literal must equal, filter exhausted before prefix means no overlap). `Validate` calls `CheckRendered(s.Clients.Worker, offset, offset+count)` once the rest of validation passed, rendering `${worker}` exactly as the engine does (`strings.ReplaceAll(..., "${worker}", s.Clients.Worker)`, so an empty worker renders as the empty string). It runs only when `clientIdDenyRegex` is set, or when `forbiddenTopicPrefixes` is set and some workload topic contains `${`; otherwise the template-literal check is enough and `Parse` stays cheap. Workers already re-run `Validate` on their partition (`internal/worker/worker.go` parses the assignment, `Engine.Run` calls `Validate` at `engine.go:33`), and `api.assign` sets `Clients.Worker` to the worker ID, so `${worker}` is checked with the real value without extra code. Warnings in `PlanWith`: the idle rule (workload sends nothing for at least the hint: `connection` or `subscriber`, or a publisher/mixed period >= hint, and effective keepAlive >= hint) and the port rule (`ceil(peak/workers) > 25000 * destinations`, destinations = option, else `len(AddressLabels)`, else 1).
- [ ] **Step 5: Failing secret tests** in `secrets_test.go` (skip on Windows):
  - `TestSecretHygieneWideMode`: keyFile 0644 -> `{File: "client.key", Problem: "permissions 0644 are wider than 0600"}`; 0600 and 0400 -> none; 0777 -> problem text also says `the filesystem may not support POSIX modes (for example WSL /mnt/c); move the key to a native filesystem`.
  - `TestSecretHygieneGitWorkTree`: keyFile under a temp dir containing `.git/` (dir) or a `.git` file -> finding mentioning `git work tree`.
  - `TestSecretMissingFileIsWarningOnly`: nonexistent keyFile -> finding `file not found on this host`; `Validate` still passes.
  - `TestSecretFindingsNeverIncludeFullPath`: no finding contains the temp dir path.
  - `TestTLSConfigStrictSecretsRefuses`: strict and a 0644 key -> error `refusing key file client.key: permissions 0644 are wider than 0600 (safety.strictSecrets)`; not strict -> loads and emits one `slog.Warn` with `file=client.key`.
- [ ] **Step 6: Implement `secrets.go`**: `os.Stat` mode `&0o077 != 0` for keyFile; walk parents of certFile/keyFile/caFile for `.git`. `TLSConfig(s, strict)` with callers updated (`NewConnector`). Inline `keyPem` (profiles and API submissions) is not file-checked. Reduce any log of `KeyFile`/`CertFile`/`CAFile` to basename (`rg -n 'KeyFile|CertFile|CAFile|KeyPEM' --type go`).
- [ ] **Step 7: CLI and API**: `validate` and `plan` parse flags the way `runScenario` does (`server.go:143`, path first or last). `plan` gains `--workers N` (default 1) and `--resolve` (calls `mqtt.ResolveBroker` with a 5s timeout and passes `len(answers)` as `Destinations`; on error prints `WARN  dns  resolution failed; using N assumed destination address(es)`). Refactor `printPlan(w io.Writer, s scenario.Scenario, p scenario.Plan)` so tests capture output. `validate` prints each `WARN` line, then secret findings as `WARN  secrets  client.key: ...`, then `VALID <name>`, and fails under `strictSecrets`. `printPlan` adds `OK    safety  2 forbidden prefixes, client ID rule checked on 10000 IDs` or `WARN  safety  no forbiddenTopicPrefixes set` (when `safety` is empty and the URL is `mqtts`/`wss`), a DNS line (`dns: resolveEachDial, 2 resolvers, pick random, ipv4`), an `addressLabels` count and the hostname verification mode. `POST /api/v1/plan` accepts optional `workers` (1 to 256) and uses `PlanWith`; its JSON already embeds `Plan.Warnings` (the struct has no JSON tags, so the key is `Warnings`). Tests: `TestPlanPrintsSafetyDNSAndWarnings`, `TestValidatePrintsWarningsBeforeValid` in `cmd/mqtitan/cli_test.go`; `TestPlanEndpointAcceptsWorkers` in `internal/api/api_test.go`.
- [ ] **Step 8: Docs**: `docs/cli.md` (`plan --workers --resolve`, validate warnings), `docs/api.md` (plan body `workers`).
- [ ] **Step 9: Run** `make check` and confirm PASS.
- [ ] **Step 10: Commit** `feat: add safety guardrails, key file hygiene and plan warnings`

### Task 7: Multi-target broker telemetry with expected targets and baseline (PR 5)

**Files:**
- Create: `internal/emqx/fleet.go`, `internal/emqx/fleet_test.go`, `internal/emqx/prometheus.go`, `internal/emqx/prometheus_test.go`
- Modify: `internal/scenario/scenario.go` (`Scenario.Telemetry`, validation), `internal/api/broker.go` (route `GET /api/v1/tests/{id}/brokers`), `internal/api/manager.go:202-295` (`execute`), `internal/api/manager.go` (`Manager` gains credentials), `cmd/mqtitan/server.go:87` (read credential env vars), `web/src/App.tsx` (`useStream` at line 1147 handles a new `broker` event), `docs/api.md`, `docs/README.md`

**Interfaces:**
- Produces:
  ```go
  // scenario
  type Telemetry struct {
      Interval        Duration          `yaml:"interval,omitempty"`        // default 15s, min 5s
      ExpectedTargets int               `yaml:"expectedTargets,omitempty"` // 0 = no check
      Baseline        string            `yaml:"baseline,omitempty"`        // "", "none", "first"
      EMQX            []EMQXTarget      `yaml:"emqx,omitempty"`
      Prometheus      *PrometheusSource `yaml:"prometheus,omitempty"`
  }
  type EMQXTarget struct { Name string `yaml:"name"`; Zone string `yaml:"zone,omitempty"`; URL string `yaml:"url"` }
  type PrometheusSource struct {
      URL         string `yaml:"url"`
      Query       string `yaml:"query"`                 // e.g. sum by (pod, zone) (emqx_live_connections_count)
      TargetLabel string `yaml:"targetLabel,omitempty"` // default "pod"
      ZoneLabel   string `yaml:"zoneLabel,omitempty"`   // default "zone"
  }
  // Scenario gains: Telemetry *Telemetry `yaml:"telemetry,omitempty"`

  // emqx
  type TargetSample struct {
      Target, Zone string
      Connections  float64 // after baseline subtraction, clamped at 0
      Raw          float64 // as reported
      Error        string  `json:",omitempty"`
  }
  type FleetSample struct { Timestamp time.Time; Source string; Targets []TargetSample; Complete bool; Baseline bool }
  type Credentials struct { APIKey, APISecret, PromToken, PromUser, PromPassword string }
  func FetchFleet(ctx context.Context, t scenario.Telemetry, c Credentials, hc *http.Client) FleetSample
  type Baseline map[string]float64 // target -> first raw value
  func (b Baseline) Apply(s FleetSample) FleetSample
  ```

- [ ] **Step 1: Failing tests**:
  - `TestFetchFleetEMQXTargets`: 3 `httptest` servers serving `/api/v5/nodes` (with `live_connections`) and `/api/v5/metrics?aggregate=false` -> 3 samples with YAML name and zone, `Complete: true`. Each target reuses `emqx.Client.Fetch`; a target is successful when `Snapshot.Connections != nil`, because `Fetch` joins a metrics-endpoint error into its return value even when nodes succeeded.
  - `TestFetchFleetPartialFailure`: one target 500 on `/api/v5/nodes` -> its sample has `Error`, `Complete: false`; no API key in the text.
  - `TestFetchFleetConcurrencyBound`: 64 targets -> at most 16 concurrent requests.
  - `TestPrometheusVector`: asserts `query` param and `Authorization: Bearer t`; result `{"metric":{"pod":"emqx-0","zone":"zone-a"},"value":[1700000000,"120"]}` -> `{emqx-0, zone-a, 120}`.
  - `TestPrometheusMissingLabels`: series without `zone` -> Zone `""` (sample still complete); without `pod` -> error `missing label pod`, `Complete: false`.
  - `TestPrometheusFewerThanExpectedTargets`: `expectedTargets: 4`, 3 series -> `Complete: false` and an error naming `3 of 4 expected targets`; 4 series -> complete; 5 series -> complete (scale-out is not a failure).
  - `TestPrometheusErrorStatus`: `{"status":"error"}` and non-vector result types -> `Complete: false`.
  - `TestBaselineFirst`: first sample `{a:100, b:80}`, later `{a:150, b:90, c:40}` -> `{a:50, b:10, c:40}`; a value below baseline clamps to 0; `Raw` keeps reported values.
  - `TestTelemetryValidation`: no sources -> error; both `emqx` and `prometheus` set -> `telemetry must set exactly one of emqx or prometheus` (both would list every pod twice and double CV and `expectedTargets`); `http://u:p@host` -> `credentials must not be in YAML`; interval `1s` -> error; duplicate names -> error; `expectedTargets: -1` or `5000` -> error; `baseline: last` -> `telemetry.baseline must be none or first`.
- [ ] **Step 2: Run** `go test ./internal/emqx ./internal/scenario -run 'Fleet|Prometheus|Baseline|Telemetry'` and confirm FAIL.
- [ ] **Step 3: Implement** `fleet.go` (semaphore of 16; per-request timeout `min(interval/2, 5s)`, matching the 5s cap in `Client.get`; `Source` is `emqx` or `prometheus`, never both; `expectedTargets` counted over that source's target list) and `prometheus.go` (`GET <url>/api/v1/query?query=...`, 1 MiB cap like `Client.MaxResponseBytes`, no redirects).
- [ ] **Step 4: Controller wiring**: `server.go` reads the credential env vars once into `emqx.Credentials` on the manager. `Manager.execute` starts a poller goroutine when `s.Telemetry != nil`. With `baseline: first`, `execute` takes one synchronous `FetchFleet` before starting the engine or worker assignments (stored with `Baseline: true`); targets absent from it, or appearing later, get baseline 0. Samples are kept on the execution (`[]emqx.FleetSample`, capped at 2880), persisted on the `Test` record at the end, and published as SSE events of kind `broker`. `GET /api/v1/tests/{id}/brokers` returns them. The global `MQTITAN_EMQX_URL` telemetry (`ConfigureEMQX`) is unchanged. The poller runs on the controller process only; workers never contact Prometheus or the EMQX API. `useStream` in `App.tsx` appends `broker` events to query key `["brokers", id]`. Test in `internal/api`: `TestTestTelemetryPolledAndStreamed`, `TestBaselineSampleTakenBeforeEngineStarts`.
- [ ] **Step 5: Docs**: `docs/api.md` adds the route and the `broker` SSE event; `docs/README.md` index row "Prove broker balance" pointing at `emqx.md#per-test-telemetry`.
- [ ] **Step 6: Run** `make check` and confirm PASS.
- [ ] **Step 7: Commit** `feat: poll per-test EMQX targets and Prometheus for broker telemetry`

### Task 8: Balance statistics and thresholds (PR 5)

**Files:**
- Create: `internal/emqx/balance.go`, `internal/emqx/balance_test.go`, `web/src/BalancePanel.tsx`, `web/src/BalancePanel.test.tsx`
- Modify: `internal/thresholds/thresholds.go` (`parse`, `Evaluate`), `internal/thresholds/thresholds_test.go`, `internal/api/manager.go` (store `Balance` on `Test`; the single `Evaluate` caller at line 253), `internal/api/prometheus.go`, `web/src/model.ts` (`Test.balance`, `runWarnings`), `web/src/App.tsx`

**Interfaces:**
- Consumes: `emqx.FleetSample` (Task 7).
- Produces:
  ```go
  type Group struct { Key string `json:"key"`; Connections float64 `json:"connections"` }
  type Balance struct {
      Timestamp   time.Time `json:"timestamp"`
      Complete    bool      `json:"complete"`
      Targets     []Group   `json:"targets"`
      Zones       []Group   `json:"zones"`       // empty unless every target has a zone
      TargetCV    float64   `json:"targetCV"`    // population stddev / mean
      TargetRatio float64   `json:"targetRatio"` // max/min; +Inf when min == 0 (JSON null)
      ZoneSkew    float64   `json:"zoneSkew"`    // (max-min)/mean of zone totals
      ZoneRatio   float64   `json:"zoneRatio"`
  }
  func ComputeBalance(s FleetSample) Balance
  func PeakBalance(samples []FleetSample) (Balance, bool) // highest total after baseline; Baseline samples excluded
  // Test gains: Balance *emqx.Balance `json:"balance,omitempty"`
  // thresholds.Evaluate(input map[string]string, s metrics.Snapshot, b *emqx.Balance)
  ```

- [ ] **Step 1: Failing balance tests**: `TestComputeBalance`: 100/100/100/100 -> CV 0, ratio 1; 50/150 -> CV 0.5, ratio 3; zones a:{100,100}, b:{200} -> skew 0; a:300, b:100 -> skew 1.0. `TestRatioZeroMin` -> `+Inf`, JSON `null`. `TestPeakBalance` picks the highest total and ignores the `Baseline: true` sample. `TestZoneStatsRequireEveryZone`: one target without zone -> `Zones` empty.
- [ ] **Step 2: Failing threshold tests**:
  - `TestBalanceThresholds`: `broker_connection_cv: "<10%"` with CV 0.05 -> pass, observed `5.000000%`; CV 0.2 -> fail. `zone_connection_skew: "<=20%"` with 0.1 -> pass.
  - `TestBalanceThresholdNoData`: `b == nil` -> both fail, `no observations`.
  - `TestBalanceThresholdFailsOnIncompleteSample`: `Complete: false` -> fail, observed `incomplete broker telemetry`.
  - `TestBalanceThresholdNeedsTwoGroups`: one target -> CV fails `no observations`; one zone, or `Zones` empty -> skew fails `no observations`.
  - `TestBalanceThresholdUsesPeakSample` (in `internal/api`): samples 300 then 0 -> `Test.Balance` is the 300 sample.
  - `TestBalancePercentMayExceed100`: `zone_connection_skew: "<150%"` parses; error-rate names keep the 0 to 100 rule.
- [ ] **Step 3: Run** `go test ./internal/emqx ./internal/thresholds ./internal/api -run 'Balance|Peak|ZoneStats|Ratio'` and confirm FAIL.
- [ ] **Step 4: Implement** `balance.go`; in `parse` add a third metric class (percent without the 100 cap); extend `Evaluate`. `Manager.execute` computes `PeakBalance` before threshold evaluation and stores it on `Test`.
- [ ] **Step 5: Prometheus**: `emqxload_broker_connections{target,zone}` (latest sample), `emqxload_broker_connection_cv`, `emqxload_zone_connection_skew_ratio`, `emqxload_broker_telemetry_complete` (0/1). Test asserts the lines.
- [ ] **Step 6: Dashboard** `BalancePanel.tsx`: per-target and per-zone bars, CV / max-min / skew tiles, an "incomplete telemetry" banner, a "zone labels missing" note when `zones` is empty; fed from `["brokers", id]` and `Test.balance`. Extend `runWarnings` in `model.ts` (added in 0.3.0) with `Broker telemetry was incomplete at the peak sample` when `balance.complete === false`. Tests: renders `CV 5.0%`; renders banner; `model.test.ts` covers the new warning.
- [ ] **Step 7: Run** `make check` and confirm PASS.
- [ ] **Step 8: Commit** `feat: add broker balance statistics and thresholds`

### Task 9: Example, docs and compose end-to-end proof (PR 6)

**Files:**
- Create: `examples/nlb-balance.yaml`, `deploy/e2e/balance/compose.yaml`, `deploy/e2e/balance/prometheus.yml`, `deploy/e2e/balance/balanced.yaml`, `deploy/e2e/balance/skewed.yaml`, `deploy/e2e/balance/nozone.yaml`, `deploy/e2e/balance/missing-target.yaml`, `deploy/e2e/balance/run.sh`
- Modify: `docs/scenario-spec.md`, `docs/emqx.md`, `docs/certificates.md`, `docs/operations.md`, `docs/README.md`, `examples/README.md`, `cmd/mqtitan/cli_test.go` (new `TestExamplesValidate`; no example-validation test exists today)

- [ ] **Step 1: Write the failing test** `TestExamplesValidate` in `cmd/mqtitan/cli_test.go`: glob `../../examples/*.yaml`, `scenario.Load` each, `thresholds.Validate`, and assert no error. Run `go test ./cmd/mqtitan -run TestExamplesValidate` and confirm it fails only because `nlb-balance.yaml` is missing (assert the file count includes it).
- [ ] **Step 2: Write `examples/nlb-balance.yaml`** with exactly: `broker.url: mqtts://broker.example.com:443`, `version: "3.1.1"`, `keepAlive: 45s`, `idleTimeoutHint: 100s`, `tls: {caFile, certFile, keyFile placeholders under /etc/mqtitan/, serverName: broker-01.zone-a.example.com, hostnameVerification: cn-fallback}`, `dns: {resolveEachDial: true, resolvers: ["192.0.2.53:53","198.51.100.53:53"], pick: random, family: ipv4}`, two `addressLabels` (`192.0.2.0/24 -> legacy-nlb`, `198.51.100.0/24 -> new-nlb-1a`), `clients.idTemplate: mqtitan-${sequence:08}`, `clients.lifetime: {min: 5m, max: 15m}`, one `connection` workload, `telemetry: {interval: 15s, expectedTargets: 3, baseline: first, prometheus: {url: http://127.0.0.1:9090, query: "sum by (pod, zone) (emqx_live_connections_count)"}}`, `thresholds: {connection_error_rate: "<1%", broker_connection_cv: "<10%", zone_connection_skew: "<15%"}`, `safety: {forbiddenTopicPrefixes: ["devices/","telemetry/","ops/"], clientIdDenyRegex: "^[0-9a-fA-F]{12}$|^[A-Z0-9]{10}$", strictSecrets: true}`. Add a commented-out low-rate publisher on `loadtest/${clientId}` (`ratePerClient: 0.02`) as the alternative to a short keepAlive. The TLS file paths make this example CLI-only, since the API rejects file paths; say so in a YAML comment.
- [ ] **Step 3: Run** `go test ./cmd/mqtitan -run TestExamplesValidate` and `for f in examples/*.yaml; do go run ./cmd/mqtitan validate "$f" && go run ./cmd/mqtitan plan "$f"; done`. Every file prints `VALID`; `nlb-balance.yaml` prints `WARN  secrets ... file not found on this host` lines but passes.
- [ ] **Step 4: Docs.** `docs/scenario-spec.md`: sections for `broker.tls.hostnameVerification`, `broker.dns`, `broker.addressLabels`, `broker.idleTimeoutHint`, `clients.lifetime`, `telemetry`, `safety`, the two thresholds and `connectFailures`, each with default, semantics and validation errors from the field table. Include: recursive resolvers cache for the record TTL, so a precise weighted-split measurement lists the hosted zone's authoritative name servers as `IP:53`; `pick: first` depends on resolver answer order (after Go's RFC 6724 ordering), so `random` is the recommended default; run `family: ipv4` and `family: ipv6` as separate runs because many workers outside a cloud lack global IPv6; resolver failover behaviour and `dnsErrors`. Replace the sentence "An explicit list of independent broker URLs is not implemented" context with a pointer to `broker.dns` and `addressLabels`. Keepalive versus proxy idle timeout: a TCP proxy or load balancer with an idle timeout closes connections that send nothing for longer than that timeout; set `keepAlive` to at most half the idle timeout, or add a low-rate publisher on a dedicated test topic, and set `idleTimeoutHint` so `plan`/`validate` warn. Source ports: with client IP preservation each worker source IP has about 28,000 ephemeral ports (Linux default 32768 to 60999) per destination `IP:port`; `plan --workers N --resolve` warns above 25,000 per destination. Safety: generic example `forbiddenTopicPrefixes: ["devices/", "telemetry/", "ops/"]` for device fleets, not hardcoded. Secrets: `strictSecrets` refuses keys on filesystems that report 0777 (for example WSL `/mnt/c`).
- [ ] **Step 5: Docs.** `docs/emqx.md`, new section "Per-test telemetry": EMQX target list versus Prometheus (Prometheus preferred for hundreds of pods; target list capped at 16 concurrent requests and meant for local or compose use); EMQX 5.8.8 `/api/v5/prometheus/stats` exposes both `emqx_connections_count` and `emqx_live_connections_count` without labels, so `pod`/`zone` come from scrape configuration; prefer `emqx_live_connections_count`. On Kubernetes the zone comes from the node, not from EMQX; document the join:
  ```promql
  sum by (pod, zone) (emqx_live_connections_count * on (pod) group_left(node) kube_pod_info{namespace="emqx"} * on (node) group_left(zone) label_replace(kube_node_labels, "zone", "$1", "label_topology_kubernetes_io_zone", "(.*)"))
  ```
  and the variant `max by (pod, node) (kube_pod_info{namespace="emqx"})` in place of `kube_pod_info{...}` to avoid many-to-many errors while a pod is replaced. kube-state-metrics only exports node labels listed in `metricLabelsAllowlist` (`nodes=[topology.kubernetes.io/zone]`); without it the zone is empty and `zone_connection_skew` fails with `no observations`. Prometheus cannot report a pod it never scraped: set `expectedTargets`. `baseline: first` judges balance on the test's own connections when other clients share the brokers. The poller runs on the controller, so the controller needs network access to Prometheus (for example `kubectl port-forward`), while workers run elsewhere. EMQX 4.x `/api/v4` targets are not supported; use Prometheus.
- [ ] **Step 6: Docs.** `docs/certificates.md`: profiles are the way to give remote workers mTLS material (API submissions reject file paths); there is no profile delete API, so use a dedicated controller database for short-lived lab credentials and delete it with its `.key` file afterwards. `docs/operations.md`: link the source-port and idle-timeout guidance. `examples/README.md`: add the `nlb-balance.yaml` row.
- [ ] **Step 7: Compose harness** `deploy/e2e/balance/compose.yaml`: three `emqx/emqx:5.8.8` services on network `lab` (fixed `ipam` subnet), each with alias `broker` (Docker DNS at `127.0.0.11` returns all three IPs); Prometheus (pinned by digest) with job `zoned` scraping each broker's `/api/v5/prometheus/stats` with static labels `pod` and `zone` (`z1`, `z1`, `z2`) and job `nozone` scraping the same brokers with only `pod`; an `mqtitan` service built from the repo root. Scenarios (all `mqtt://broker:1883`, `dns: {resolveEachDial: true, resolvers: ["127.0.0.11:53"]}`, `addressLabels` for the lab subnet, `lifetime: {min: 20s, max: 40s}`, 2-minute run, `expectedTargets: 3`, query `sum by (pod, zone) (emqx_live_connections_count{job="zoned"})`):
  - `balanced.yaml`: `pick: random`, `broker_connection_cv: "<35%"`.
  - `skewed.yaml`: `resolveEachDial: false`, `pick: first`, `broker_connection_cv: "<10%"`. Resolving once and caching sends every connection to one broker IP. Do not rely on `pick: first` with `resolveEachDial: true` for skew: Docker's embedded DNS shuffles answers for a shared alias, so that run would look balanced.
  - `nozone.yaml`: `pick: random`, query on `job="nozone"`, `zone_connection_skew: "<50%"`.
  - `missing-target.yaml`: `pick: random`, `expectedTargets: 4`, `broker_connection_cv: "<35%"`.
- [ ] **Step 8: Metric regression check** before relying on the name: `docker compose -f deploy/e2e/balance/compose.yaml up -d broker-a && docker compose -f deploy/e2e/balance/compose.yaml exec broker-a curl -s localhost:18083/api/v5/prometheus/stats | grep -E '^emqx_(live_)?connections_count'`. Expect both lines (confirmed on 5.8.8 during review).
- [ ] **Step 9: `run.sh`** runs each scenario with `docker compose run --rm mqtitan run /e2e/<file>` and asserts: `balanced` exits 0, results show 3 remote addresses each with more than 20% of connects, `churned > 0`, `churnReconnects > 0`, `dnsAnswers` with 3 keys, `connectFailures` empty; `skewed` exits non-zero with `broker_connection_cv` failed; `nozone` exits non-zero with `zone_connection_skew` observed `no observations`; `missing-target` exits non-zero with observed `incomplete broker telemetry`. Run it and expect `PASS balanced`, `PASS skewed-fails`, `PASS nozone-fails`, `PASS missing-target-fails`.
- [ ] **Step 10: Final checks**: `make check`, `make build`, `helm lint deploy/helm/mqtitan`, `docker build .`. All pass.
- [ ] **Step 11: Commit** `docs: add NLB balance example, scenario docs and compose e2e`

## Not in scope

- EMQX 4.x management API targets (`/api/v4`); Prometheus covers both fleets.
- Kubernetes pod discovery for the EMQX target list; use Prometheus for large fleets.
- Per-client connection rows (only aggregates are kept).
- Resolver hostnames in `broker.dns.resolvers`; only `IP:port`.
- DNS over TCP/TLS/HTTPS, EDNS tuning and a configurable per-resolver timeout (fixed at 2s).
- Hostname verification controls in the web wizard; the field is YAML-only in this plan.
- Certificate profile deletion or rotation APIs.
- Automatic zone discovery from cloud APIs; zone comes from telemetry labels or the EMQX target list.
- Hardcoded forbidden topic prefixes or client ID rules for any fleet.
- Counting idle-timeout drops separately from other broker disconnects; they appear as `disconnect` errors.

## Impact of 0.3.0 / PR #31

PR #31 is the release-please PR for 0.3.0 (`CHANGELOG.md`, `.release-please-manifest.json`, `Chart.yaml version`); all code came from #30, merged to main as `f72159e`. Changes to this plan because of it:

- **TLS file paths rejected over the API** (`decodeScenario`/`decodeLocalScenario`, `StartLocal`). Secret file hygiene (Task 6) now targets CLI and local runs only; `/api/v1/plan` cannot carry file paths; the example is documented as CLI-only; remote distributed runs use certificate profiles (field checklist updated).
- **Error categories array with `timeout`** (`errorCategories`). Task 2 follows the same fixed-array pattern for stages; connect timeouts get the `timeout` stage in `connectFailures`, separate from the existing operation `timeout` category.
- **`everConnected` in `runClient`.** Task 5 extends it with a `sessionKind` instead of the old `session > 0` logic.
- **Gzip worker reports with 2 MiB / 8 MiB bounds and report sequencing.** Task 4 adds a size test; no protocol change is needed.
- **`runWarnings` in `web/src/model.ts`.** Task 8 reuses it for the incomplete-telemetry warning.
- **`lostWorkers`, worker token, registration session.** Field checklist requires one release on controller and workers and `MQTITAN_WORKER_TOKEN`.
- **Fixed-schedule publisher with `publishSlotsMissed`.** The low-rate keepalive publisher in the example uses it unchanged.
- **Line references.** `execute` moved to `manager.go:202-295`, `runClient` to `engine.go:137-192`, `runSession` to `engine.go:202-364`; `Evaluate` is called at `manager.go:253`.
- **Release mechanics.** No version or changelog edits; these `feat:` PRs produce the next minor release after 0.3.0. Observation, not acted on: PR #31 bumps `Chart.yaml version` to 0.3.0 but leaves `appVersion: 0.2.0`.

## Verification record

Checked by reading the code at `9cba605` (PR #31 head) and diffing against `c02b3b4`:

- Confirmed: `TLSConfig` at `client.go:30`, `Connector` at `:70`, `NewConnector` at `:76`, `dial` at `:111-161`, `connect3` at `:191`, `connect5` at `:287`; `Broker`, `TLS`, `Reconnect`, `Clients`, `Plan` structs and `Validate` in `scenario.go`; `Engine.Run` at `engine.go:32`; `prometheus.go` `emqxload_` naming; `POST /api/v1/plan` at `http.go:223`; `MQTITAN_EMQX_URL` at `server.go:87`; `printPlan` at `main.go:160`; `emqx.Client.Fetch` reads `/api/v5/nodes` and `/api/v5/metrics?aggregate=false`; `golang.org/x/net v0.60.0` direct in `go.mod`; module path `github.com/mqtitan/mqtitan`.
- Confirmed by experiment: Go 1.26.10 `x509.Certificate.Verify` with no `DNSName` and `ExtKeyUsageServerAuth` accepts a CN-only broker leaf (EKU serverAuth, no SAN) against its issuing CA, while `VerifyHostname` fails with the legacy Common Name error. EMQX 5.8.8 `/api/v5/prometheus/stats` serves both connection gauges unlabelled and without authentication by default; `/api/v5/nodes` returns 401 without an API key.

Corrections made to the previous plan:

1. `engine.go:137-260` was wrong: `runClient` is `137-192`, `runSession` is `202-364`.
2. `manager.go:202-270` (`execute`) is `202-295`.
3. "Engine test fakes" and "the existing fake connector" do not exist; engine tests use in-process mochi brokers and only `client3`/`client5` implement `Client`.
4. `Close()` does not send MQTT DISCONNECT on either protocol; Task 5 adds `Disconnect()` instead of "verify and add if not".
5. paho.golang returns an error together with the CONNACK on refusal, so `connect5`'s `CONNACK reason %d` branch never runs for refusals; Task 2 fixes the order.
6. `TestResolverRoundRobin` as written would see 2 queries per lookup (A and AAAA); it now uses `family: ipv4`.
7. `LookupNetIP` may return IPv4-mapped addresses; `Unmap()` is now required.
8. `pick: first` follows Go's RFC 6724-ordered result, not raw resolver order; docs say so.
9. The engine-side `CheckRendered(realWorker, ...)` call is unnecessary: workers already re-run `Validate` on partitions that carry `Clients.Worker`.
10. `Plan` is built with a positional literal (`scenario.go:306`); adding fields requires a keyed literal.
11. `printPlan` writes to stdout directly; tests need the `io.Writer` refactor.
12. `useStream` handles only `metrics` and `status`; the `broker` event needs handling, and TypeScript types live in `web/src/model.ts`.
13. Fleet success must test `Snapshot.Connections != nil`, not `err == nil`.
14. "Group empty zones as unknown" would turn missing labels into a measurable zone; zone stats now require every target to have a zone.
15. Repo-facing text referenced a specific vendor's topic tree; replaced with generic prefixes and fixtures.
16. Example `keepAlive: 180s` would be dropped by a 100s idle proxy with a connection-only workload; changed to 45s with `idleTimeoutHint: 100s`.
17. `docs/README.md` and `docs/api.md` updates were missing although CONTRIBUTING.md requires them.
18. No example-validation test existed; Task 9 creates `TestExamplesValidate`.
19. `Classify` checked timeouts before the DNS stage, so a resolver timeout would be counted as `timeout` while `TestResolverFailover` expected `dns`; the DNS stage now wins.
20. `resolvers[next.Add(1)-1 % n]` parsed as `next.Add(1) - (1 % n)`; parenthesised.
21. Lookups used the bare host, so `search`/`ndots` from `/etc/resolv.conf` would send suffixed names to public resolvers; lookups now use the rooted name. The `resolvers: []` case is now specified.
22. `CheckRendered` used the literal `worker` for an empty `clients.worker` while the engine renders it as empty, so a MAC-like ID could pass validation; it now renders exactly as the engine does, and only when a rule needs rendering.
23. Configuring both telemetry sources would list each pod twice; validation now requires exactly one.
24. `skewed.yaml` relied on `pick: first`, but Docker DNS shuffles alias answers; it now resolves once and caches.
25. Environment-specific open questions, context notes and the field checklist moved to a private notes file outside the repository, because this plan is tracked in a public repository.
