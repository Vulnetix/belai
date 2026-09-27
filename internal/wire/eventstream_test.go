package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
)

func TestEventStreamRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(EncodeKiroEvent("assistantResponseEvent", []byte(`{"content":"hi"}`)))
	buf.Write(EncodeKiroEvent("toolUseEvent", []byte(`{"toolUseId":"t1"}`)))
	r := NewEventStreamReader(&buf)

	m, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if m.EventType() != "assistantResponseEvent" || m.MessageType() != "event" || string(m.Payload) != `{"content":"hi"}` {
		t.Fatalf("frame 1 = %+v %q", m.Headers, m.Payload)
	}
	m, err = r.Next()
	if err != nil || m.EventType() != "toolUseEvent" {
		t.Fatalf("frame 2 = %+v, %v", m.Headers, err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestEventStreamRejectsBadChecksums(t *testing.T) {
	frame := EncodeKiroEvent("assistantResponseEvent", []byte(`{"content":"hi"}`))

	payloadFlip := append([]byte(nil), frame...)
	payloadFlip[len(payloadFlip)-6] ^= 0xff
	if _, err := NewEventStreamReader(bytes.NewReader(payloadFlip)).Next(); !errors.Is(err, ErrEventFrame) {
		t.Fatalf("payload corruption: want ErrEventFrame, got %v", err)
	}

	preludeFlip := append([]byte(nil), frame...)
	preludeFlip[9] ^= 0xff
	if _, err := NewEventStreamReader(bytes.NewReader(preludeFlip)).Next(); !errors.Is(err, ErrEventFrame) {
		t.Fatalf("prelude corruption: want ErrEventFrame, got %v", err)
	}
}

func TestEventStreamRejectsOversizeFrame(t *testing.T) {
	var prelude []byte
	prelude = binary.BigEndian.AppendUint32(prelude, MaxEventFrame+1)
	prelude = binary.BigEndian.AppendUint32(prelude, 0)
	prelude = binary.BigEndian.AppendUint32(prelude, crc32IEEE(prelude))
	if _, err := NewEventStreamReader(bytes.NewReader(prelude)).Next(); !errors.Is(err, ErrEventFrame) {
		t.Fatalf("want ErrEventFrame, got %v", err)
	}
}

func TestEventStreamTruncatedFrame(t *testing.T) {
	frame := EncodeKiroEvent("assistantResponseEvent", []byte(`{"content":"hi"}`))
	_, err := NewEventStreamReader(bytes.NewReader(frame[:len(frame)-3])).Next()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want ErrUnexpectedEOF, got %v", err)
	}
}

func TestEventStreamSkipsNonStringHeaders(t *testing.T) {
	// A bool, an int32 and a uuid header before the string one.
	var hb []byte
	hb = append(hb, 1, 'a', 0)
	hb = append(hb, 1, 'b', 4, 0, 0, 0, 7)
	hb = append(hb, 1, 'c', 9)
	hb = append(hb, make([]byte, 16)...)
	hb = append(hb, 11)
	hb = append(hb, ":event-type"...)
	hb = append(hb, 7, 0, 3)
	hb = append(hb, "xyz"...)
	payload := []byte("{}")
	total := uint32(12 + len(hb) + len(payload) + 4)
	var out []byte
	out = binary.BigEndian.AppendUint32(out, total)
	out = binary.BigEndian.AppendUint32(out, uint32(len(hb)))
	out = binary.BigEndian.AppendUint32(out, crc32IEEE(out))
	out = append(out, hb...)
	out = append(out, payload...)
	out = binary.BigEndian.AppendUint32(out, crc32IEEE(out))

	m, err := NewEventStreamReader(bytes.NewReader(out)).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if m.EventType() != "xyz" || len(m.Headers) != 1 {
		t.Fatalf("headers = %+v", m.Headers)
	}
}

func crc32IEEE(b []byte) uint32 { return crc32.ChecksumIEEE(b) }
