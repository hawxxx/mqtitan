# syntax=docker/dockerfile:1
FROM node:22-alpine AS ui
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mqtitan ./cmd/mqtitan

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 mqtitan && adduser -D -u 10001 -G mqtitan mqtitan && mkdir /data && chown mqtitan:mqtitan /data
WORKDIR /app
COPY --from=build /out/mqtitan /usr/local/bin/mqtitan
COPY --from=ui /src/web/dist /app/web/dist
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["mqtitan"]
CMD ["serve", "--listen", "0.0.0.0:8080", "--data", "/data/mqtitan.db", "--web", "/app/web/dist"]
