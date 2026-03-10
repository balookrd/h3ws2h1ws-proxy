FROM golang:1.26.1-alpine AS build

ARG REPO_URL=https://github.com/balookrd/ws-quic-proxy.git
ARG REPO_REF=main

WORKDIR /src

RUN apk add --no-cache git ca-certificates

RUN git clone --depth=1 --branch ${REPO_REF} ${REPO_URL} .

RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/ws-quic-proxy ./cmd/ws-quic-proxy

FROM gcr.io/distroless/static-debian12

WORKDIR /app

ENV GODEBUG="http2xconnect=1"

COPY --from=build /out/ws-quic-proxy /app/ws-quic-proxy

ENTRYPOINT ["/app/ws-quic-proxy"]
