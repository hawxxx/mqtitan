.PHONY: build web test check
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
web:
	cd web && npm ci && npm run build
build: web
	go build -ldflags "-X main.version=$(VERSION)" -o bin/mqtitan ./cmd/mqtitan
test:
	go test ./...
	cd web && npm test -- --run
check:
	go test -race ./...
	go vet ./...
	cd web && npm test -- --run && npm run build
