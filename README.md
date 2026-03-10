# ws-quic-proxy

A proxy server that accepts **WebSocket over HTTP/3** (RFC 9220 Extended CONNECT) on ingress and forwards traffic to a classic **WebSocket over HTTP/1.1** backend.

## Features

- Accepts WebSocket sessions over **HTTP/3 Extended CONNECT**.
- Proxies bidirectional traffic between H3 clients and backend `ws://` / `wss://` services.
- Enforces limits for:
  - single WebSocket frame size (`-max-frame`),
  - assembled WebSocket message size (`-max-message`),
  - total concurrent sessions (`-max-conns`).
- Proxies control frames (`ping`, `pong`, `close`) and closes sessions gracefully.
- Exposes Prometheus metrics on a dedicated endpoint (`/metrics`).
- Exposes health check endpoints (`/health/tcp`, `/health/udp`).

## Throughput improvements in this version

- Added a shared Gorilla WebSocket **write buffer pool** for backend connections to reduce allocations and GC pressure under high concurrency.
- Increased the H3 frame reader buffer from `64 KiB` to `256 KiB` to reduce read overhead on large/fragmented payload streams.
- Removed an extra allocation/copy when forwarding reassembled fragmented messages from H3 to backend.
- Kept backend per-message compression disabled to reduce CPU usage at high RPS.

## Project structure

### `cmd/ws-quic-proxy/main.go`
Minimal entrypoint: calls `app.Run()` and exits on error.

### `internal/run.go`
Application bootstrap:
- parses flags,
- validates backend URL,
- starts metrics endpoint,
- creates and starts the HTTP/3 server.

### `internal/config/config.go`
Contains:
- `Config` — process configuration,
- `Limits` — runtime proxy limits,
- `DefaultTLSConfig()` — TLS 1.3 + ALPN for HTTP/3.

### `internal/metrics/metrics.go`
Defines and registers Prometheus metrics for:
- active sessions,
- accepted/rejected connections,
- stage-specific errors,
- proxied bytes and messages,
- control frames,
- oversize drops.

### `internal/proxy/proxy.go`
Core session handling:
- RFC9220 header checks,
- handshake response (`Sec-WebSocket-Accept`),
- backend WS dial,
- bidirectional pump goroutines,
- session lifecycle and errors.

### `internal/proxy/pumps.go`
Data transfer logic:
- `pumpH3ToBackend` — from H3 stream to backend WebSocket,
- `pumpBackendToH3` — from backend WebSocket to H3 stream.

Includes:
- fragmented message assembly,
- timeout handling,
- `ping/pong/close` handling,
- size limit enforcement.

### `internal/ws/framing.go`
Low-level RFC6455 framing:
- frame read (`ReadFrame`),
- data/control/close frame write,
- large payload fragmentation,
- mask/unmask,
- close payload parsing.

### `internal/ws/utils.go`
Helpers:
- `ComputeAccept` — `Sec-WebSocket-Accept` calculation,
- `PickFirstToken` — first subprotocol selection,
- `IsNetClose` — heuristic for normal connection close.

## Run

### Requirements
- Go 1.25+
- TLS certificate and key (`cert.pem`, `key.pem`) for the HTTP/3 server.

### Example

```bash
go run ./cmd/ws-quic-proxy \
  -listen :443 \
  -cert cert.pem \
  -key key.pem \
  -backend ws://127.0.0.1:8080 \
  -path "^/ws/(tcp|udp)$" \
  -metrics 127.0.0.1:9090
```

### Docker example

```bash
docker build -t ws-quic-proxy:local .

docker run --rm \
  -p 443:443/udp \
  -p 9090:9090 \
  -v "$(pwd)/cert.pem:/app/cert.pem:ro" \
  -v "$(pwd)/key.pem:/app/key.pem:ro" \
  ws-quic-proxy:local \
  -listen :443 \
  -cert /app/cert.pem \
  -key /app/key.pem \
  -backend ws://host.docker.internal:8080 \
  -path "^/ws/(tcp|udp)$" \
  -metrics :9090
```

> On Linux, you may need `--add-host=host.docker.internal:host-gateway` so the container can reach backend services on the host.

## Main flags

- `-listen` — UDP address for the HTTP/3 server (default `:443`)
- `-cert` / `-key` — TLS certificate and key
- `-backend` — backend WebSocket URL (`ws://` or `wss://`) without path
  - Path and query are always taken from incoming requests.
- `-path` — regexp for RFC9220 CONNECT path validation (default `^/ws$`)
- `-metrics` — metrics endpoint address (disabled by default)
- `-max-frame` — maximum bytes in a single frame
- `-max-message` — maximum bytes in an assembled message
- `-max-conns` — maximum concurrent sessions
- `-read-timeout` / `-write-timeout` — read/write timeouts
- `-debug` — verbose debug logs for handshake and proxy traffic

## Metrics

Endpoint: `http://<metrics-addr>/metrics` (available only if `-metrics` is set)

Health check endpoints (main HTTP/3 listener):
- `/health/tcp` → `GET`: `200 OK` + `ok`; `CONNECT`: `200 OK`
- `/health/udp` → `GET`: `200 OK` + `ok`; `CONNECT`: `200 OK`

Key metrics:
- `h3ws_proxy_active_sessions`
- `h3ws_proxy_accepted_total`
- `h3ws_proxy_rejected_total{reason=...}`
- `h3ws_proxy_errors_total{stage=...}`
- `h3ws_proxy_bytes_total{dir=...}`
- `h3ws_proxy_messages_total{dir=...,type=...}`
- `h3ws_proxy_frames_total{dir=...,opcode=...}`
- `h3ws_proxy_message_size_bytes_bucket{dir=...,type=...,le=...}`
- `h3ws_proxy_session_duration_seconds_bucket{le=...}`
- `h3ws_proxy_session_traffic_bytes_bucket{dir=...,le=...}`
- `h3ws_proxy_control_frames_total{type=...}`
- `h3ws_proxy_oversize_drops_total{kind=...}`

## Troubleshooting

### Error: `expected first frame to be a HEADERS frame`

If you see logs such as:

- `Application error 0x101 (local): expected first frame to be a HEADERS frame`
- `read response headers failed ... expected first frame to be a HEADERS frame`

this indicates a failure **before** the application request handler starts.

In `quic-go`, this can happen when:
- the first request stream frame is not `HEADERS`, or
- `HEADERS` exists but header block/QPACK decoding fails.

Per RFC 9114 / RFC 9220, expected order on a client-initiated bidirectional stream:
1. `HEADERS` (`:method=CONNECT` for Extended CONNECT)
2. then `DATA` / WebSocket payload

Check on the client/gateway side:
- real HTTP/3 framing is used,
- request stream starts with `HEADERS`,
- QPACK/header block is decoded correctly,
- intermediate gateways do not rewrite the stream into an incompatible format.

Relevant metrics:
- `h3ws_proxy_prerequest_close_total{reason="request_stream_invalid_first_frame_or_headers"}`
- `h3ws_proxy_prerequest_close_total{reason=...}`
- `h3ws_proxy_errors_total{stage="h3_framing"}`

## Docker + Grafana

Included files for local observability stack:

- `Dockerfile` — proxy container build/run.
- `docker-compose.yml` — runs `h3ws-proxy`, `prometheus`, `grafana`.
- `deploy/prometheus/prometheus.yml` — Prometheus scrape config.
- `deploy/grafana/provisioning/datasources/prometheus.yml` — datasource provisioning.
- `deploy/grafana/provisioning/dashboards/dashboards.yml` — dashboard auto-import.
- `deploy/grafana/dashboards/h3ws-proxy-overview.json` — ready-to-use dashboard.

Run:

```bash
docker compose up --build
```

After startup:
- Grafana: `http://localhost:3000` (`admin/admin`)
- Prometheus: `http://localhost:9091`
- Proxy metrics: `http://localhost:9090/metrics`

> In the compose setup, a self-signed TLS certificate is generated automatically into `./certs` on first run.
