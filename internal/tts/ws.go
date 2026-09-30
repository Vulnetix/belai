package tts

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// A minimal RFC 6455 client: enough to speak to one service. It follows no
// redirect (any answer but 101 is an error the caller can read), masks every
// frame it sends, answers pings, reassembles fragments and bounds every size.

const (
	wsGUID        = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsMaxFrame    = 4 << 20
	wsMaxMessage  = 16 << 20
	wsReadTimeout = 30 * time.Second
)

const (
	opCont   = 0x0
	opText   = 0x1
	opBinary = 0x2
	opClose  = 0x8
	opPing   = 0x9
	opPong   = 0xA
)

// errHandshake carries the HTTP answer to a refused upgrade.
type errHandshake struct{ Resp *http.Response }

func (e *errHandshake) Error() string {
	return fmt.Sprintf("websocket upgrade refused: HTTP %d", e.Resp.StatusCode)
}

type wsConn struct {
	c  net.Conn
	br *bufio.Reader
}

// dialWS opens a websocket to u (wss, or ws to a loopback host in tests).
func dialWS(ctx context.Context, u *url.URL, hdr http.Header) (*wsConn, error) {
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	conn := raw
	if u.Scheme == "wss" {
		tc := tls.Client(raw, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		conn = tc
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		conn.Close()
		return nil, err
	}
	k := base64.StdEncoding.EncodeToString(key)
	req := &http.Request{Method: "GET", URL: u, Host: u.Host, Header: http.Header{}}
	for name, vals := range hdr {
		req.Header[name] = vals
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", k)
	_ = conn.SetDeadline(time.Now().Add(wsReadTimeout))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(k + wsGUID))
	if resp.StatusCode != http.StatusSwitchingProtocols ||
		resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		conn.Close()
		return nil, &errHandshake{Resp: resp}
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{c: conn, br: br}, nil
}

func (w *wsConn) Close() error { return w.c.Close() }

// writeFrame sends one masked, unfragmented frame.
func (w *wsConn) writeFrame(op byte, payload []byte) error {
	hdr := []byte{0x80 | op}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	hdr = append(hdr, mask...)
	body := make([]byte, len(payload))
	for i, b := range payload {
		body[i] = b ^ mask[i%4]
	}
	_ = w.c.SetWriteDeadline(time.Now().Add(wsReadTimeout))
	_, err := w.c.Write(append(hdr, body...))
	return err
}

// WriteText sends a text message.
func (w *wsConn) WriteText(s string) error { return w.writeFrame(opText, []byte(s)) }

// ReadMessage returns the next text or binary message. Pings are answered and
// a close frame ends with io.EOF.
func (w *wsConn) ReadMessage() (op byte, msg []byte, err error) {
	var buf []byte
	var first byte
	for {
		_ = w.c.SetReadDeadline(time.Now().Add(wsReadTimeout))
		fin, fop, payload, err := w.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch fop {
		case opPing:
			if err := w.writeFrame(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = w.writeFrame(opClose, nil)
			return 0, nil, closeError(payload)
		case opText, opBinary:
			if buf != nil {
				return 0, nil, errors.New("websocket: new message inside a fragmented one")
			}
			first = fop
		case opCont:
			if buf == nil {
				return 0, nil, errors.New("websocket: continuation with nothing to continue")
			}
		default:
			return 0, nil, fmt.Errorf("websocket: unknown opcode %d", fop)
		}
		if len(buf)+len(payload) > wsMaxMessage {
			return 0, nil, errors.New("websocket: message too large")
		}
		buf = append(buf, payload...)
		if buf == nil {
			buf = []byte{}
		}
		if fin {
			return first, buf, nil
		}
	}
}

func (w *wsConn) readFrame() (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(w.br, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0F
	if h[0]&0x70 != 0 {
		err = errors.New("websocket: reserved bits set")
		return
	}
	if h[1]&0x80 != 0 {
		err = errors.New("websocket: server frame is masked")
		return
	}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(w.br, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(w.br, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > wsMaxFrame || (op >= opClose && n > 125) {
		err = errors.New("websocket: frame too large")
		return
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(w.br, payload)
	return
}

// closeError describes a close frame: its status code and reason, which is
// what the service says when it refuses a request after the upgrade. It
// matches io.EOF, so callers that only care that the stream ended still work.
func closeError(payload []byte) error {
	if len(payload) < 2 {
		return io.EOF
	}
	reason := strings.Map(func(r rune) rune {
		if r >= ' ' && r != 0x7f {
			return r
		}
		return -1
	}, string(payload[2:]))
	if len(reason) > 120 {
		reason = reason[:120]
	}
	return fmt.Errorf("closed by the server (code %d %q): %w", binary.BigEndian.Uint16(payload), reason, io.EOF)
}
