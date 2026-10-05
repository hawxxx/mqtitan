.PHONY: build web test check
web:
	cd web && npm ci && npm run build
build: web
	go build -o bin/mqtitan ./cmd/mqtitan
test:
	go test ./...
	cd web && npm test -- --run
check:
	go test -race ./...
	go vet ./...
	cd web && npm test -- --run && npm run build
