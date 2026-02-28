package ws

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"h3ws2h1ws-proxy/internal/metrics"
)

const (
	OpCont   = 0x0
	OpText   = 0x1
	OpBinary = 0x2
	OpClose  = 0x8
	OpPing   = 0x9
	OpPong   = 0xA
)

type Frame struct {
	Fin     bool
	Opcode  byte
	Masked  bool
	Payload []byte
}

func ReadFrame(r *bufio.Reader, maxFramePayload int64) (Frame, error) {
	var f Frame

	b0, err := r.ReadByte()
	if err != nil {
		return f, err
	}
	b1, err := r.ReadByte()
	if err != nil {
		return f, err
	}

	f.Fin = (b0 & 0x80) != 0
	f.Opcode = b0 & 0x0F
	f.Masked = (b1 & 0x80) != 0

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
		metrics.OversizeDrops.WithLabelValues("frame").Inc()
		return f, fmt.Errorf("frame too large: %d", plen)
	}

	var maskKey [4]byte
	if f.Masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return f, err
		}
	}

	f.Payload = make([]byte, plen)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return f, err
	}

	if f.Masked {
		for i := range f.Payload {
			f.Payload[i] ^= maskKey[i%4]
		}
	}
	return f, nil
}

func WriteDataFrame(w io.Writer, opcode byte, payload []byte, masked bool, maxFramePayload int64) error {
	if maxFramePayload <= 0 || int64(len(payload)) <= maxFramePayload {
		return writeFrame(w, opcode, payload, masked, true)
	}

	remaining := payload
	first := true
	for int64(len(remaining)) > maxFramePayload {
		chunk := remaining[:maxFramePayload]
		remaining = remaining[maxFramePayload:]

		op := opcode
		if !first {
			op = OpCont
		}
		first = false
		if err := writeFrame(w, op, chunk, masked, false); err != nil {
			return err
		}
	}
	op := opcode
	if !first {
		op = OpCont
	}
	return writeFrame(w, op, remaining, masked, true)
}

func WriteControlFrame(w io.Writer, opcode byte, payload []byte) error {
	if len(payload) > 125 {
		payload = payload[:125]
	}
	return writeFrame(w, opcode, payload, false, true)
}

func WriteCloseFrame(w io.Writer, code uint16, reason string) error {
	pl := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(pl[:2], code)
	copy(pl[2:], []byte(reason))
	if len(pl) > 125 {
		pl = pl[:125]
	}
	return writeFrame(w, OpClose, pl, false, true)
}

func writeFrame(w io.Writer, opcode byte, payload []byte, masked bool, fin bool) error {
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

func ParseClosePayload(p []byte) (int, string) {
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
