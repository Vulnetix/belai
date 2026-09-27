package kanban

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
)

// The board file is:
//
//	magic   "BKAN"                4 bytes
//	version uint16, big endian    2 bytes
//	payload encoding/gob(Board)   n bytes
//	sum     SHA-256(payload)      32 bytes
//
// gob is the standard library's self-describing Go encoding: it decodes
// straight into the typed structs and ignores fields it does not know, so the
// schema can grow without a migration. The model never reads these bytes;
// the tools render items as text.
const (
	magic         = "BKAN"
	formatVersion = 1
	headerLen     = len(magic) + 2
)

// Encode serialises a board.
func Encode(b Board) ([]byte, error) {
	var payload bytes.Buffer
	if err := gob.NewEncoder(&payload).Encode(b); err != nil {
		return nil, fmt.Errorf("encode kanban: %w", err)
	}
	sum := sha256.Sum256(payload.Bytes())
	out := make([]byte, 0, headerLen+payload.Len()+len(sum))
	out = append(out, magic...)
	out = binary.BigEndian.AppendUint16(out, formatVersion)
	out = append(out, payload.Bytes()...)
	out = append(out, sum[:]...)
	return out, nil
}

// Decode parses a board file. Any mismatch — magic, version, checksum, gob —
// is an error; the caller must never overwrite a file it could not read.
func Decode(data []byte) (Board, error) {
	if len(data) < headerLen+sha256.Size {
		return Board{}, errors.New("file too short")
	}
	if string(data[:len(magic)]) != magic {
		return Board{}, errors.New("not a kanban board (bad magic)")
	}
	if v := binary.BigEndian.Uint16(data[len(magic):headerLen]); v != formatVersion {
		return Board{}, fmt.Errorf("unsupported board version %d (this Belai reads %d)", v, formatVersion)
	}
	payload := data[headerLen : len(data)-sha256.Size]
	want := data[len(data)-sha256.Size:]
	if sum := sha256.Sum256(payload); !bytes.Equal(sum[:], want) {
		return Board{}, errors.New("checksum mismatch")
	}
	var b Board
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&b); err != nil {
		return Board{}, fmt.Errorf("decode: %w", err)
	}
	return b, nil
}
