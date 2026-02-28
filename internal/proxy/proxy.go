package proxy

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"h3ws2h1ws-proxy/internal/config"
	"h3ws2h1ws-proxy/internal/metrics"
	"h3ws2h1ws-proxy/internal/ws"

	"github.com/gorilla/websocket"
	"github.com/quic-go/quic-go/http3"
)

type Proxy struct {
	Backend *url.URL
	Limits  config.Limits
	active  int64
}

func (p *Proxy) HandleH3WebSocket(w http.ResponseWriter, r *http.Request) {
	if atomic.AddInt64(&p.active, 1) > p.Limits.MaxConns {
		atomic.AddInt64(&p.active, -1)
		metrics.Rejected.WithLabelValues("max_conns").Inc()
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer atomic.AddInt64(&p.active, -1)

	if strings.ToUpper(r.Method) != http.MethodConnect {
		metrics.Rejected.WithLabelValues("method").Inc()
		http.Error(w, "expected CONNECT", http.StatusMethodNotAllowed)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	ver := r.Header.Get("Sec-WebSocket-Version")
	if key == "" || ver != "13" {
		metrics.Rejected.WithLabelValues("bad_headers").Inc()
		http.Error(w, "missing/invalid websocket headers", http.StatusBadRequest)
		return
	}

	hs, ok := r.Body.(http3.HTTPStreamer)
	if !ok {
		metrics.Errors.WithLabelValues("no_stream_takeover").Inc()
		http.Error(w, "http3 stream takeover not supported", http.StatusInternalServerError)
		return
	}
	stream := hs.HTTPStream()
	defer func() { _ = stream.Close() }()

	w.Header().Set("Sec-WebSocket-Accept", ws.ComputeAccept(key))
	subp := r.Header.Get("Sec-WebSocket-Protocol")
	if subp != "" {
		w.Header().Set("Sec-WebSocket-Protocol", ws.PickFirstToken(subp))
	}
	w.WriteHeader(http.StatusOK)

	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment}
	backendHeader := http.Header{}
	if subp != "" {
		backendHeader.Set("Sec-WebSocket-Protocol", ws.PickFirstToken(subp))
	}
	bws, resp, err := dialer.Dial(p.Backend.String(), backendHeader)
	if err != nil {
		metrics.Errors.WithLabelValues("backend_dial").Inc()
		if resp != nil {
			log.Printf("backend dial failed: %v (status=%s)", err, resp.Status)
		} else {
			log.Printf("backend dial failed: %v", err)
		}
		_ = ws.WriteCloseFrame(stream, 1011, "backend dial failed")
		return
	}
	defer func() { _ = bws.Close() }()

	metrics.Accepted.Inc()
	metrics.ActiveSessions.Inc()
	defer metrics.ActiveSessions.Dec()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	bws.SetReadLimit(p.Limits.MaxMessageSize)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- pumpH3ToBackend(ctx, stream, bws, p.Limits)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- pumpBackendToH3(ctx, bws, stream, p.Limits)
	}()

	err1 := <-errCh
	cancel()
	_ = stream.Close()
	_ = bws.Close()
	wg.Wait()

	if err1 != nil && !errors.Is(err1, context.Canceled) && !ws.IsNetClose(err1) {
		metrics.Errors.WithLabelValues("session").Inc()
		log.Printf("session ended: %v", err1)
	}
}
