package ws

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"strings"
)

func ComputeAccept(key string) string {
	const magic = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	h := sha1.Sum([]byte(key + magic))
	return base64.StdEncoding.EncodeToString(h[:])
}

func PickFirstToken(v string) string {
	parts := strings.Split(v, ",")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func IsNetClose(err error) bool {
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
