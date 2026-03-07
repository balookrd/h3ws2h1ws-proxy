package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"h3ws2h1ws-proxy/internal/config"
	"h3ws2h1ws-proxy/internal/proxy"
)

func TestNewProxyHandlerHealthEndpoints(t *testing.T) {
	t.Parallel()

	cfg := config.Config{PathRegexp: regexp.MustCompile(`^/ws$`)}
	h := newProxyHandler(cfg, &proxy.Proxy{}, nil)

	tests := []struct {
		name   string
		path   string
		status int
		body   string
	}{
		{name: "root", path: "/", status: http.StatusOK, body: "ok\n"},
		{name: "health tcp", path: "/health/tcp", status: http.StatusOK, body: "ok\n"},
		{name: "health udp", path: "/health/udp", status: http.StatusOK, body: "ok\n"},
		{name: "not found", path: "/health", status: http.StatusNotFound, body: "404 page not found\n"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()

			h.ServeHTTP(rr, req)

			if rr.Code != tc.status {
				t.Fatalf("status: got %d, want %d", rr.Code, tc.status)
			}
			if rr.Body.String() != tc.body {
				t.Fatalf("body: got %q, want %q", rr.Body.String(), tc.body)
			}
		})
	}
}
