# syntax=docker/dockerfile:1
# Base images are pinned by digest; Dependabot (docker ecosystem) bumps them.
FROM --platform=$BUILDPLATFORM node:23-alpine@sha256:a34e14ef1df25b58258956049ab5a71ea7f0d498e41d0b514f4b8de09af09456 AS ui
WORKDIR /src/web
RUN --mount=type=bind,source=web/package.json,target=package.json \
    --mount=type=bind,source=web/package-lock.json,target=package-lock.json \
    --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Cross-compile on the build host; no QEMU needed for arm64.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine@sha256:9ce231b28998b32e116927c61aa5c406183061d4f3f2d61b292c88e7f6bdf7ca AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
RUN --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/mqtitan ./cmd/mqtitan

FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 AS runtime
RUN --mount=type=cache,target=/etc/apk/cache,sharing=locked \
    apk upgrade && apk add ca-certificates && \
    addgroup -g 10001 mqtitan && adduser -D -u 10001 -G mqtitan mqtitan && \
    mkdir /data && chown 10001:10001 /data
WORKDIR /app
COPY --link --from=build /out/mqtitan /usr/local/bin/mqtitan
COPY --link --from=ui /src/web/dist /app/web/dist
USER 10001:10001
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["mqtitan"]
CMD ["serve", "--listen", "0.0.0.0:8080", "--data", "/data/mqtitan.db", "--web", "/app/web/dist"]
