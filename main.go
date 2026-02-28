package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// ---------------- Metrics ----------------

var (
	mActiveSessions = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "h3ws_proxy_active_sessions",
		Help: "Number of active proxy sessions",
	})
	mAccepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "h3ws_proxy_accepted_total",
		Help: "Accepted RFC9220 sessions",
	})
	mRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_rejected_total",
		Help: "Rejected requests by reason",
	}, []string{"reason"})
	mErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_errors_total",
		Help: "Errors by stage",
	}, []string{"stage"})
	mBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_bytes_total",
		Help: "Bytes forwarded by direction",
	}, []string{"dir"}) // h3_to_h1, h1_to_h3
	mMessages = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_messages_total",
		Help: "Messages forwarded by direction and type",
	}, []string{"dir", "type"}) // text/binary
	mCtrl = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_control_frames_total",
		Help: "Control frames observed",
	}, []string{"type"}) // ping/pong/close
	mOversizeDrops = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "h3ws_proxy_oversize_drops_total",
		Help: "Dropped frames/messages due to size limits",
	}, []string{"kind"}) // frame/message
)

func init() {
	prometheus.MustRegister(
		mActiveSessions, mAccepted, mRejected, mErrors,
		mBytes, mMessages, mCtrl, mOversizeDrops,
	)
}

// ---------------- Config ----------------

type Limits struct {
	MaxFrameSize   int64 // per frame payload
	MaxMessageSize int64 // reassembled message (text/binary)
	MaxConns       int64 // simple global cap
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

type Proxy struct {
	Backend *url.URL
	Limits  Limits

	// simple global connection cap
	active int64
}

func main() {
	var (
		listenAddr = flag.String("listen", ":443", "UDP listen addr for HTTP/3 (e.g. :443, :8443)")
		certFile   = flag.String("cert", "cert.pem", "TLS cert PEM")
		keyFile    = flag.String("key", "key.pem", "TLS key PEM")

		backendWS = flag.String("backend", "ws://127.0.0.1:8080/ws", "backend ws:// or wss:// URL (HTTP/1.1 WebSocket)")
		path      = flag.String("path", "/ws", "path to accept RFC9220 websocket CONNECT")

		metricsAddr = flag.String("metrics", "127.0.0.1:9090", "TCP addr for Prometheus /metrics")
		maxFrame    = flag.Int64("max-frame", 1<<20, "max ws frame payload bytes (H3 side)")
		maxMessage  = flag.Int64("max-message", 8<<20, "max reassembled message bytes (H3 side)")
		maxConns    = flag.Int64("max-conns", 2000, "max concurrent sessions")
		readTO      = flag.Duration("read-timeout", 120*time.Second, "read timeout")
		writeTO     = flag.Duration("write-timeout", 15*time.Second, "write timeout")
	)
	flag.Parse()

	backendURL, err := url.Parse(*backendWS)
	if err != nil {
		log.Fatalf("bad -backend: %v", err)
	}
	if backendURL.Scheme != "ws" && backendURL.Scheme != "wss" {
		log.Fatalf("backend scheme must be ws or wss, got %q", backendURL.Scheme)
	}

	// metrics server (plain HTTP, keep internal)
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		srv := &http.Server{
			Addr:              *metricsAddr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		log.Printf("metrics listening on http://%s/metrics", *metricsAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("metrics server error: %v", err)
		}
	}()

	p := &Proxy{
		Backend: backendURL,
		Limits: Limits{
			MaxFrameSize:   *maxFrame,
			MaxMessageSize: *maxMessage,
			MaxConns:       *maxConns,
			ReadTimeout:    *readTO,
			WriteTimeout:   *writeTO,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(*path, p.handleH3WebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok\n"))
	})

	tlsConf := &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{http3.NextProtoH3},
	}

	quicConf := &quic.Config{
		EnableDatagrams: false,
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 20 * time.Second,
	}

	server := http3.Server{
		Addr:       *listenAddr,
		Handler:    mux,
		TLSConfig:  tlsConf,
		QUICConfig: quicConf,
	}

	log.Printf("HTTP/3 WS proxy listening on udp %s, path=%s, backend=%s", *listenAddr, *path, backendURL.String())
	if err := server.ListenAndServeTLS(*certFile, *keyFile); err != nil {
		log.Fatalf("ListenAndServeTLS: %v", err)
	}
}

func (p *Proxy) handleH3WebSocket(w http.ResponseWriter, r *http.Request) {
	// Simple global cap
	if atomic.AddInt64(&p.active, 1) > p.Limits.MaxConns {
		atomic.AddInt64(&p.active, -1)
		mRejected.WithLabelValues("max_conns").Inc()
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	defer atomic.AddInt64(&p.active, -1)

	// RFC9220 = extended CONNECT, expect CONNECT + Sec-WebSocket-* headers
	if strings.ToUpper(r.Method) != "CONNECT" {
		mRejected.WithLabelValues("method").Inc()
		http.Error(w, "expected CONNECT", http.StatusMethodNotAllowed)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	ver := r.Header.Get("Sec-WebSocket-Version")
	if key == "" || ver != "13" {
		mRejected.WithLabelValues("bad_headers").Inc()
		http.Error(w, "missing/invalid websocket headers", http.StatusBadRequest)
		return
	}

	hs, ok := r.Body.(http3.HTTPStreamer)
	if !ok {
		mErrors.WithLabelValues("no_stream_takeover").Inc()
		http.Error(w, "http3 stream takeover not supported", http.StatusInternalServerError)
		return
	}
	stream := hs.HTTPStream()
	defer func() { _ = stream.Close() }()

	// Accept the tunnel
	w.Header().Set("Sec-WebSocket-Accept", computeAccept(key))
	subp := r.Header.Get("Sec-WebSocket-Protocol")
	if subp != "" {
		w.Header().Set("Sec-WebSocket-Protocol", pickFirstToken(subp))
	}
	w.WriteHeader(http.StatusOK)

	// Dial backend WS (HTTP/1.1)
	dialer := websocket.Dialer{
		Proxy: http.ProxyFromEnvironment,
	}
	backendHeader := http.Header{}
	if subp != "" {
		backendHeader.Set("Sec-WebSocket-Protocol", pickFirstToken(subp))
	}
	bws, resp, err := dialer.Dial(p.Backend.String(), backendHeader)
	if err != nil {
		mErrors.WithLabelValues("backend_dial").Inc()
		if resp != nil {
			log.Printf("backend dial failed: %v (status=%s)", err, resp.Status)
		} else {
			log.Printf("backend dial failed: %v", err)
		}
		_ = writeCloseFrame(stream, 1011, "backend dial failed")
		return
	}
	defer func() { _ = bws.Close() }()

	mAccepted.Inc()
	mActiveSessions.Inc()
	defer mActiveSessions.Dec()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Timeouts on backend ws
	bws.SetReadLimit(p.Limits.MaxMessageSize)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// H3 -> H1
	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- pumpH3ToBackend(ctx, stream, bws, p.Limits)
	}()

	// H1 -> H3
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

	if err1 != nil && !errors.Is(err1, context.Canceled) && !isNetClose(err1) {
		mErrors.WithLabelValues("session").Inc()
		log.Printf("session ended: %v", err1)
	}
}

// ---------------- Pumps ----------------

func pumpH3ToBackend(ctx context.Context, s io.ReadWriter, bws *websocket.Conn, lim Limits) error {
	br := bufio.NewReader(s)

	var (
		assembling   bool
		assemOpcode  byte
		assemPayload []byte
	)

	flushMessage := func(op byte, msg []byte) error {
		// forward to backend
		bws.SetWriteDeadline(time.Now().Add(lim.WriteTimeout))
		switch op {
		case opText:
			mMessages.WithLabelValues("h3_to_h1", "text").Inc()
			mBytes.WithLabelValues("h3_to_h1").Add(float64(len(msg)))
			return bws.WriteMessage(websocket.TextMessage, msg)
		case opBinary:
			mMessages.WithLabelValues("h3_to_h1", "binary").Inc()
			mBytes.WithLabelValues("h3_to_h1").Add(float64(len(msg)))
			return bws.WriteMessage(websocket.BinaryMessage, msg)
		default:
			return nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// best-effort read timeout: if underlying stream is a net.Conn this will work; for QUIC streams it may not.
		// (we still have QUIC idle timeout at transport level)
		f, err := readWSFrame(br, lim.MaxFrameSize)
		if err != nil {
			return err
		}

		switch f.opcode {
		case opText, opBinary:
			// start a (possibly fragmented) message
			if assembling {
				return errors.New("protocol error: new data frame while assembling")
			}
			if f.fin {
				if int64(len(f.payload)) > lim.MaxMessageSize {
					mOversizeDrops.WithLabelValues("message").Inc()
					_ = writeCloseFrame(s, 1009, "message too big")
					return errors.New("message too big")
				}
				if err := flushMessage(f.opcode, f.payload); err != nil {
					return err
				}
				continue
			}
			assembling = true
			assemOpcode = f.opcode
			assemPayload = append(assemPayload[:0], f.payload...)
			if int64(len(assemPayload)) > lim.MaxMessageSize {
				mOversizeDrops.WithLabelValues("message").Inc()
				_ = writeCloseFrame(s, 1009, "message too big")
				return errors.New("message too big")
			}

		case opCont:
			if !assembling {
				return errors.New("protocol error: continuation without start")
			}
			assemPayload = append(assemPayload, f.payload...)
			if int64(len(assemPayload)) > lim.MaxMessageSize {
				mOversizeDrops.WithLabelValues("message").Inc()
				_ = writeCloseFrame(s, 1009, "message too big")
				return errors.New("message too big")
			}
			if f.fin {
				// complete reassembled message
				msg := make([]byte, len(assemPayload))
				copy(msg, assemPayload)
				assembling = false
				assemPayload = assemPayload[:0]
				if err := flushMessage(assemOpcode, msg); err != nil {
					return err
				}
			}

		case opPing:
			mCtrl.WithLabelValues("ping").Inc()
			// Reply to client ping with pong on H3 side
			if err := writeControlFrame(s, opPong, f.payload); err != nil {
				return err
			}
			// optionally ping backend too
			_ = bws.WriteControl(websocket.PingMessage, f.payload, time.Now().Add(5*time.Second))

		case opPong:
			mCtrl.WithLabelValues("pong").Inc()
			_ = bws.WriteControl(websocket.PongMessage, f.payload, time.Now().Add(5*time.Second))

		case opClose:
			mCtrl.WithLabelValues("close").Inc()
			code, reason := parseClosePayload(f.payload)
			_ = bws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(5*time.Second))
			_ = writeCloseFrame(s, uint16(code), reason)
			return io.EOF

		default:
			// ignore
		}
	}
}

func pumpBackendToH3(ctx context.Context, bws *websocket.Conn, s io.Writer, lim Limits) error {
	// forward backend control frames to client where reasonable
	bws.SetPingHandler(func(appData string) error {
		mCtrl.WithLabelValues("ping").Inc()
		// ping from backend -> ping frame to client
		_ = writeControlFrame(s, opPing, []byte(appData))
		// also respond pong to backend default behavior
		return bws.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	bws.SetPongHandler(func(appData string) error {
		mCtrl.WithLabelValues("pong").Inc()
		_ = writeControlFrame(s, opPong, []byte(appData))
		return nil
	})
	bws.SetCloseHandler(func(code int, text string) error {
		mCtrl.WithLabelValues("close").Inc()
		_ = writeCloseFrame(s, uint16(code), text)
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		bws.SetReadDeadline(time.Now().Add(lim.ReadTimeout))
		mt, data, err := bws.ReadMessage()
		if err != nil {
			// propagate close best-effort
			if ce, ok := err.(*websocket.CloseError); ok {
				_ = writeCloseFrame(s, uint16(ce.Code), ce.Text)
			} else {
				_ = writeCloseFrame(s, 1011, "backend read error")
			}
			return err
		}

		// enforce outgoing size too
		if int64(len(data)) > lim.MaxMessageSize {
			mOversizeDrops.WithLabelValues("message").Inc()
			_ = writeCloseFrame(s, 1009, "message too big")
			return errors.New("backend message too big")
		}

		switch mt {
		case websocket.TextMessage:
			mMessages.WithLabelValues("h1_to_h3", "text").Inc()
			mBytes.WithLabelValues("h1_to_h3").Add(float64(len(data)))
			// send as single frame (you can optionally fragment here)
			if err := writeDataFrame(s, opText, data, false, lim.MaxFrameSize); err != nil {
				return err
			}
		case websocket.BinaryMessage:
			mMessages.WithLabelValues("h1_to_h3", "binary").Inc()
			mBytes.WithLabelValues("h1_to_h3").Add(float64(len(data)))
			if err := writeDataFrame(s, opBinary, data, false, lim.MaxFrameSize); err != nil {
				return err
			}
		default:
			// ignore
		}
	}
}

// ---------------- WebSocket framing (RFC6455) ----------------

const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

type wsFrame struct {
	fin     bool
	opcode  byte
	masked  bool
	payload []byte
}

func readWSFrame(r *bufio.Reader, maxFramePayload int64) (wsFrame, error) {
	var f wsFrame

	b0, err := r.ReadByte()
	if err != nil {
		return f, err
	}
	b1, err := r.ReadByte()
	if err != nil {
		return f, err
	}

	f.fin = (b0 & 0x80) != 0
	f.opcode = b0 & 0x0F
	f.masked = (b1 & 0x80) != 0

	plen := int64(b1 & 0x7F)
	switch plen {
	case 126:
		var tmp [2]byte
		if _, err := io.ReadFull(r, tmp[:]); err != nil {
			return f, err
		}
		plen = int64(binary.BigEndian.Uint16(tmp[:]))
	case 127:
		var tmp [8]byte
		if _, err := io.ReadFull(r, tmp[:]); err != nil {
			return f, err
		}
		plen = int64(binary.BigEndian.Uint64(tmp[:]))
		if plen < 0 {
			return f, errors.New("invalid length")
		}
	}

	if maxFramePayload > 0 && plen > maxFramePayload {
		mOversizeDrops.WithLabelValues("frame").Inc()
		// still need to drain mask+payload to keep stream consistent? In strict proxy we should close.
		return f, fmt.Errorf("frame too large: %d", plen)
	}

	var maskKey [4]byte
	if f.masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return f, err
		}
	}

	f.payload = make([]byte, plen)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return f, err
	}

	if f.masked {
		for i := range f.payload {
			f.payload[i] ^= maskKey[i%4]
		}
	}
	return f, nil
}

// writeDataFrame supports optional fragmentation by maxFramePayload when >0.
// For server->client (H3 side) frames are typically unmasked.
func writeDataFrame(w io.Writer, opcode byte, payload []byte, masked bool, maxFramePayload int64) error {
	// If no fragmentation requested/needed
	if maxFramePayload <= 0 || int64(len(payload)) <= maxFramePayload {
		return writeFrame(w, opcode, payload, masked, true)
	}

	// Fragment: first frame opcode, subsequent are continuation
	remaining := payload
	first := true
	for int64(len(remaining)) > maxFramePayload {
		chunk := remaining[:maxFramePayload]
		remaining = remaining[maxFramePayload:]

		op := opcode
		if !first {
			op = opCont
		}
		first = false
		// not final
		if err := writeFrame(w, op, chunk, masked, false); err != nil {
			return err
		}
	}
	// final chunk
	op := opcode
	if !first {
		op = opCont
	}
	return writeFrame(w, op, remaining, masked, true)
}

func writeControlFrame(w io.Writer, opcode byte, payload []byte) error {
	// Control frames must be <=125 bytes
	if len(payload) > 125 {
		payload = payload[:125]
	}
	return writeFrame(w, opcode, payload, false, true)
}

func writeCloseFrame(w io.Writer, code uint16, reason string) error {
	pl := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(pl[:2], code)
	copy(pl[2:], []byte(reason))
	if len(pl) > 125 {
		pl = pl[:125]
	}
	return writeFrame(w, opClose, pl, false, true)
}

func writeFrame(w io.Writer, opcode byte, payload []byte, masked bool, fin bool) error {
	// RSV=0
	b0 := opcode & 0x0F
	if fin {
		b0 |= 0x80
	}

	var hdr []byte
	var b1 byte
	if masked {
		b1 = 0x80
	}

	n := len(payload)
	switch {
	case n <= 125:
		b1 |= byte(n)
		hdr = []byte{b0, b1}
	case n <= 65535:
		b1 |= 126
		hdr = make([]byte, 4)
		hdr[0], hdr[1] = b0, b1
		binary.BigEndian.PutUint16(hdr[2:], uint16(n))
	default:
		b1 |= 127
		hdr = make([]byte, 10)
		hdr[0], hdr[1] = b0, b1
		binary.BigEndian.PutUint64(hdr[2:], uint64(n))
	}

	if _, err := w.Write(hdr); err != nil {
		return err
	}

	if masked {
		var key [4]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		if _, err := w.Write(key[:]); err != nil {
			return err
		}
		m := make([]byte, len(payload))
		copy(m, payload)
		for i := range m {
			m[i] ^= key[i%4]
		}
		_, err := w.Write(m)
		return err
	}

	_, err := w.Write(payload)
	return err
}

func parseClosePayload(p []byte) (int, string) {
	if len(p) < 2 {
		return 1000, ""
	}
	code := int(binary.BigEndian.Uint16(p[:2]))
	reason := ""
	if len(p) > 2 {
		reason = string(p[2:])
	}
	return code, reason
}

func computeAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}

func pickFirstToken(v string) string {
	parts := strings.Split(v, ",")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func isNetClose(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && !ne.Temporary() {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "closed") || strings.Contains(s, "EOF") || strings.Contains(s, "canceled")
}
