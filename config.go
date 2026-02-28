package main

import (
	"crypto/tls"
	"time"

	"github.com/quic-go/quic-go/http3"
)

type Config struct {
	ListenAddr   string
	CertFile     string
	KeyFile      string
	BackendWS    string
	Path         string
	MetricsAddr  string
	MaxFrame     int64
	MaxMessage   int64
	MaxConns     int64
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

type Limits struct {
	MaxFrameSize   int64
	MaxMessageSize int64
	MaxConns       int64
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

func defaultTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{http3.NextProtoH3},
	}
}
