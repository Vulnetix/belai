package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// AWS event-stream framing (application/vnd.amazon.eventstream), the binary
// response shape of the Kiro / CodeWhisperer generateAssistantResponse call.
// Each frame is:
//
//	total_length  uint32
//	headers_length uint32
//	prelude_crc   uint32   CRC32 (IEEE) of the first 8 bytes
//	headers       [headers_length]byte
//	payload       [total_length - headers_length - 16]byte
//	message_crc   uint32   CRC32 of every byte before it
//
// A frame whose checksums do not verify, or whose length is out of bounds, is
// an error: it is never skipped, because a desynchronised reader would read
// provider bytes as frame structure.

// MaxEventFrame caps one event-stream frame. Kiro's events carry a text delta
// or a tool-input fragment, far below this.
const MaxEventFrame = 1 << 20

const eventPreludeLen = 12

// EventMessage is one decoded event-stream frame. Only string-typed headers
// are kept; the rest are skipped after their length is checked.
type EventMessage struct {
	Headers map[string]string
	Payload []byte
}

// EventType is the :event-type header.
func (m EventMessage) EventType() string { return m.Headers[":event-type"] }

// MessageType is the :message-type header ("event", "exception" or "error").
func (m EventMessage) MessageType() string { return m.Headers[":message-type"] }

// EventStreamReader reads event-stream frames from r.
type EventStreamReader struct {
	r io.Reader
}

// NewEventStreamReader wraps r.
func NewEventStreamReader(r io.Reader) *EventStreamReader {
	return &EventStreamReader{r: r}
}

// ErrEventFrame reports a malformed event-stream frame.
var ErrEventFrame = errors.New("malformed event-stream frame")

// Next returns the next frame. It returns io.EOF only at a clean frame
// boundary; a stream cut mid-frame is io.ErrUnexpectedEOF.
func (e *EventStreamReader) Next() (EventMessage, error) {
	var prelude [eventPreludeLen]byte
	if _, err := io.ReadFull(e.r, prelude[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return EventMessage{}, io.EOF
		}
		return EventMessage{}, err
	}
	total := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	if crc32.ChecksumIEEE(prelude[:8]) != binary.BigEndian.Uint32(prelude[8:12]) {
		return EventMessage{}, fmt.Errorf("%w: prelude checksum mismatch", ErrEventFrame)
	}
	if total < eventPreludeLen+4 || total > MaxEventFrame || uint64(headersLen) > uint64(total)-eventPreludeLen-4 {
		return EventMessage{}, fmt.Errorf("%w: length %d (headers %d) out of bounds", ErrEventFrame, total, headersLen)
	}
	frame := make([]byte, total)
	copy(frame, prelude[:])
	if _, err := io.ReadFull(e.r, frame[eventPreludeLen:]); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return EventMessage{}, err
	}
	body := frame[:total-4]
	if crc32.ChecksumIEEE(body) != binary.BigEndian.Uint32(frame[total-4:]) {
		return EventMessage{}, fmt.Errorf("%w: message checksum mismatch", ErrEventFrame)
	}
	headers, err := parseEventHeaders(frame[eventPreludeLen : eventPreludeLen+headersLen])
	if err != nil {
		return EventMessage{}, err
	}
	return EventMessage{Headers: headers, Payload: body[eventPreludeLen+headersLen:]}, nil
}

// parseEventHeaders decodes the header block. Every value's length is checked
// against the block before it is read.
func parseEventHeaders(b []byte) (map[string]string, error) {
	out := map[string]string{}
	need := func(n int) error {
		if len(b) < n {
			return fmt.Errorf("%w: truncated header", ErrEventFrame)
		}
		return nil
	}
	for len(b) > 0 {
		nameLen := int(b[0])
		if err := need(1 + nameLen + 1); err != nil {
			return nil, err
		}
		name := string(b[1 : 1+nameLen])
		typ := b[1+nameLen]
		b = b[2+nameLen:]
		var skip int
		switch typ {
		case 0, 1: // bool true / false
		case 2:
			skip = 1
		case 3:
			skip = 2
		case 4:
			skip = 4
		case 5, 8: // int64, timestamp
			skip = 8
		case 9: // uuid
			skip = 16
		case 6, 7: // bytes, string
			if err := need(2); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(b[:2]))
			if err := need(2 + n); err != nil {
				return nil, err
			}
			if typ == 7 {
				out[name] = string(b[2 : 2+n])
			}
			b = b[2+n:]
			continue
		default:
			return nil, fmt.Errorf("%w: unknown header type %d", ErrEventFrame, typ)
		}
		if err := need(skip); err != nil {
			return nil, err
		}
		b = b[skip:]
	}
	return out, nil
}

// EncodeEventFrame builds one event-stream frame with string headers. It is
// used by tests and mock servers; header order follows the keys slice.
func EncodeEventFrame(keys []string, headers map[string]string, payload []byte) []byte {
	var hb []byte
	for _, k := range keys {
		v := headers[k]
		hb = append(hb, byte(len(k)))
		hb = append(hb, k...)
		hb = append(hb, 7)
		hb = binary.BigEndian.AppendUint16(hb, uint16(len(v)))
		hb = append(hb, v...)
	}
	total := uint32(eventPreludeLen + len(hb) + len(payload) + 4)
	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, total)
	out = binary.BigEndian.AppendUint32(out, uint32(len(hb)))
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:8]))
	out = append(out, hb...)
	out = append(out, payload...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out))
}

// EncodeKiroEvent is EncodeEventFrame for a Kiro "event" message of eventType.
func EncodeKiroEvent(eventType string, payload []byte) []byte {
	return EncodeEventFrame(
		[]string{":event-type", ":content-type", ":message-type"},
		map[string]string{":event-type": eventType, ":content-type": "application/json", ":message-type": "event"},
		payload,
	)
}
