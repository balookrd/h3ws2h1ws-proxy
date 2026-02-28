package main

import (
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func main() {
	cfg := parseConfig()

	backendURL, err := url.Parse(cfg.BackendWS)
	if err != nil {
		log.Fatalf("bad -backend: %v", err)
	}
	if backendURL.Scheme != "ws" && backendURL.Scheme != "wss" {
		log.Fatalf("backend scheme must be ws or wss, got %q", backendURL.Scheme)
	}

	startMetricsServer(cfg.MetricsAddr)

	p := &Proxy{
		Backend: backendURL,
		Limits: Limits{
			MaxFrameSize:   cfg.MaxFrame,
			MaxMessageSize: cfg.MaxMessage,
			MaxConns:       cfg.MaxConns,
			ReadTimeout:    cfg.ReadTimeout,
			WriteTimeout:   cfg.WriteTimeout,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Path, p.handleH3WebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	tlsConf := defaultTLSConfig()
	quicConf := defaultQUICConfig()
	server := http3.Server{
		Addr:       cfg.ListenAddr,
		Handler:    mux,
		TLSConfig:  tlsConf,
		QUICConfig: quicConf,
	}

	log.Printf("HTTP/3 WS proxy listening on udp %s, path=%s, backend=%s", cfg.ListenAddr, cfg.Path, backendURL.String())
	if err := server.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile); err != nil {
		log.Fatalf("ListenAndServeTLS: %v", err)
	}
}

func parseConfig() Config {
	var cfg Config

	flag.StringVar(&cfg.ListenAddr, "listen", ":443", "UDP listen addr for HTTP/3 (e.g. :443, :8443)")
	flag.StringVar(&cfg.CertFile, "cert", "cert.pem", "TLS cert PEM")
	flag.StringVar(&cfg.KeyFile, "key", "key.pem", "TLS key PEM")

	flag.StringVar(&cfg.BackendWS, "backend", "ws://127.0.0.1:8080/ws", "backend ws:// or wss:// URL (HTTP/1.1 WebSocket)")
	flag.StringVar(&cfg.Path, "path", "/ws", "path to accept RFC9220 websocket CONNECT")

	flag.StringVar(&cfg.MetricsAddr, "metrics", "127.0.0.1:9090", "TCP addr for Prometheus /metrics")
	flag.Int64Var(&cfg.MaxFrame, "max-frame", 1<<20, "max ws frame payload bytes (H3 side)")
	flag.Int64Var(&cfg.MaxMessage, "max-message", 8<<20, "max reassembled message bytes (H3 side)")
	flag.Int64Var(&cfg.MaxConns, "max-conns", 2000, "max concurrent sessions")
	flag.DurationVar(&cfg.ReadTimeout, "read-timeout", 120*time.Second, "read timeout")
	flag.DurationVar(&cfg.WriteTimeout, "write-timeout", 15*time.Second, "write timeout")
	flag.Parse()

	return cfg
}

func startMetricsServer(addr string) {
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		srv := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		log.Printf("metrics listening on http://%s/metrics", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("metrics server error: %v", err)
		}
	}()
}

func defaultQUICConfig() *quic.Config {
	return &quic.Config{
		EnableDatagrams: false,
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 20 * time.Second,
	}
}
