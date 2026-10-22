# Changelog

## [0.3.0](https://github.com/hawxxx/mqtitan/compare/v0.2.1...v0.3.0) (2026-10-22)


### ⚠ BREAKING CHANGES

* workers and controller must run the same release. Workers must present the session returned by registration on every heartbeat, and reports are gzip-compressed; older workers cannot heartbeat to this controller and newer workers are rejected by older controllers.

### Features

* add NLB balance testing with DNS controls, churn, safety and broker balance thresholds ([#32](https://github.com/hawxxx/mqtitan/issues/32)) ([80feb55](https://github.com/hawxxx/mqtitan/commit/80feb5542fce173d2890903e94735b5f4f9b1fdc))
* **web:** add MQTiTan logo branding ([#33](https://github.com/hawxxx/mqtitan/issues/33)) ([6aa3311](https://github.com/hawxxx/mqtitan/commit/6aa3311c41fc24992799af3076deb37bbca404cc))


### Bug Fixes

* distributed, security, measurement and accessibility hardening ([#30](https://github.com/hawxxx/mqtitan/issues/30)) ([f72159e](https://github.com/hawxxx/mqtitan/commit/f72159e021ea6be575f90921385edc3568a3ca4a))

## [0.2.1](https://github.com/hawxxx/mqtitan/compare/v0.2.0...v0.2.1) (2026-10-21)


### Bug Fixes

* update Go 1.26.10 image and golang.org/x/net 0.60.0 ([#20](https://github.com/hawxxx/mqtitan/issues/20)) ([da34f8b](https://github.com/hawxxx/mqtitan/commit/da34f8bce9287b6564590642465a7fd436ee7f56))

## [0.2.0](https://github.com/hawxxx/mqtitan/compare/v0.1.0...v0.2.0) (2026-10-21)


### Features

* add latency and subscribe thresholds, flexible CLI arguments ([f50d784](https://github.com/hawxxx/mqtitan/commit/f50d7840c5d1e9d635ab6e2e013adfe5aed6f039))
* add multi-arch GHCR release pipeline and harden image ([1766e36](https://github.com/hawxxx/mqtitan/commit/1766e36e82e916e1f0aadaa9d95756970b637759))
* automate versioning and image releases with release-please ([f409f7f](https://github.com/hawxxx/mqtitan/commit/f409f7fb6fb2ffca18dc594f3e854ceb92e8f55e))
* bootstrap MQTitan MQTT load-testing platform ([d5a705d](https://github.com/hawxxx/mqtitan/commit/d5a705dd0b1bc57160b905a3dd50b2813265e23d))
* plan warnings and compiled payload validation ([761af3a](https://github.com/hawxxx/mqtitan/commit/761af3afcb49c9dc6560d2f6997269e7e36c752a))
* rerun finished tests without replacing their results ([19404e6](https://github.com/hawxxx/mqtitan/commit/19404e65b96b079503085ecd95ce19e7a0ff9b21))
* **web:** polish dashboard charts, wizard, compare and errors ([57d7e39](https://github.com/hawxxx/mqtitan/commit/57d7e39b049a8a1867e28c86879e87cbac74fcdd))


### Bug Fixes

* guard sample rates and report CLI transport errors ([87e17a6](https://github.com/hawxxx/mqtitan/commit/87e17a6ef0a350888509d30af85677bf382a3253))
* harden subscribe, reconnect and remote run handling ([3e286a1](https://github.com/hawxxx/mqtitan/commit/3e286a1ab961d33123eba59c080f960e27650658))
