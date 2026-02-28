package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

func pumpH3ToBackend(ctx context.Context, s io.ReadWriter, bws *websocket.Conn, lim Limits) error {
	br := bufio.NewReader(s)

	var (
		assembling   bool
		assemOpcode  byte
		assemPayload []byte
	)

	flushMessage := func(op byte, msg []byte) error {
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

		f, err := readWSFrame(br, lim.MaxFrameSize)
		if err != nil {
			return err
		}

		switch f.opcode {
		case opText, opBinary:
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
			if err := writeControlFrame(s, opPong, f.payload); err != nil {
				return err
			}
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
		}
	}
}

func pumpBackendToH3(ctx context.Context, bws *websocket.Conn, s io.Writer, lim Limits) error {
	bws.SetPingHandler(func(appData string) error {
		mCtrl.WithLabelValues("ping").Inc()
		_ = writeControlFrame(s, opPing, []byte(appData))
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
			if ce, ok := err.(*websocket.CloseError); ok {
				_ = writeCloseFrame(s, uint16(ce.Code), ce.Text)
			} else {
				_ = writeCloseFrame(s, 1011, "backend read error")
			}
			return err
		}

		if int64(len(data)) > lim.MaxMessageSize {
			mOversizeDrops.WithLabelValues("message").Inc()
			_ = writeCloseFrame(s, 1009, "message too big")
			return errors.New("backend message too big")
		}

		switch mt {
		case websocket.TextMessage:
			mMessages.WithLabelValues("h1_to_h3", "text").Inc()
			mBytes.WithLabelValues("h1_to_h3").Add(float64(len(data)))
			if err := writeDataFrame(s, opText, data, false, lim.MaxFrameSize); err != nil {
				return err
			}
		case websocket.BinaryMessage:
			mMessages.WithLabelValues("h1_to_h3", "binary").Inc()
			mBytes.WithLabelValues("h1_to_h3").Add(float64(len(data)))
			if err := writeDataFrame(s, opBinary, data, false, lim.MaxFrameSize); err != nil {
				return err
			}
		}
	}
}
