package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/offload"
)

// File format: magic, version (uint32, big endian), a gob payload, and the
// SHA-256 of everything before it. A file failing any check is refused and
// never overwritten.
const (
	magic          = "BELAIKB1"
	formatVersion  = 1
	maxIndexFile   = 256 << 20
	trailerBytes   = sha256.Size
	headerBytes    = len(magic) + 4
	indexFileName  = "index.bin"
	indexFileMode  = 0o600
	indexDirMode   = 0o700
	partFileSuffix = ".part"
)

// ErrCorrupt is returned for an index file that fails its magic, version or
// SHA-256 check. The file is left in place.
var ErrCorrupt = errors.New("knowledge: index file failed its integrity check")

type snapshot struct {
	Name   string
	Docs   []Doc
	Chunks []storedChunk
}

// Path is where the index for dir is kept.
func Path(dir string) string { return filepath.Join(dir, indexFileName) }

// Load reads the index stored in dir. A missing file is an empty index named
// name. A symlinked file or directory, an oversized file, or a failed
// integrity check is an error and changes nothing.
func Load(dir, name string) (*Index, error) {
	p := Path(dir)
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return NewIndex(name), nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("knowledge: %s is not a regular file", p)
	}
	if info.Size() > maxIndexFile {
		return nil, ErrCorrupt
	}
	if err := refuseSymlinkDir(dir); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(data) < headerBytes+trailerBytes || string(data[:len(magic)]) != magic {
		return nil, ErrCorrupt
	}
	body, sum := data[:len(data)-trailerBytes], data[len(data)-trailerBytes:]
	want := sha256.Sum256(body)
	if !bytes.Equal(want[:], sum) {
		return nil, ErrCorrupt
	}
	if binary.BigEndian.Uint32(body[len(magic):headerBytes]) != formatVersion {
		return nil, ErrCorrupt
	}
	var snap snapshot
	if err := gob.NewDecoder(bytes.NewReader(body[headerBytes:])).Decode(&snap); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	ix := NewIndex(name)
	ix.docs, ix.chunks = snap.Docs, snap.Chunks
	for _, d := range ix.docs {
		ix.tokens += d.Tokens
	}
	for _, c := range ix.chunks {
		if int(c.Doc) < 0 || int(c.Doc) >= len(ix.docs) {
			return nil, ErrCorrupt
		}
	}
	return ix, nil
}

// Save writes the index to dir atomically through a .part file, mode 0600,
// refusing a symlinked directory or target. An existing file that does not pass
// its own integrity check is never overwritten.
func (ix *Index) Save(dir string) error {
	if err := os.MkdirAll(dir, indexDirMode); err != nil {
		return err
	}
	if err := refuseSymlinkDir(dir); err != nil {
		return err
	}
	p := Path(dir)
	if _, err := os.Lstat(p); err == nil {
		if _, err := Load(dir, ix.Name()); err != nil {
			return err
		}
	}
	ix.mu.RLock()
	snap := snapshot{Name: ix.name, Docs: ix.docs, Chunks: ix.chunks}
	var buf bytes.Buffer
	buf.WriteString(magic)
	var v [4]byte
	binary.BigEndian.PutUint32(v[:], formatVersion)
	buf.Write(v[:])
	err := gob.NewEncoder(&buf).Encode(snap)
	ix.mu.RUnlock()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(buf.Bytes())
	buf.Write(sum[:])
	if buf.Len() > maxIndexFile {
		return fmt.Errorf("knowledge: index would be %d bytes, over the %d limit", buf.Len(), maxIndexFile)
	}
	part := p + partFileSuffix
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, indexFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			_ = os.Remove(part)
			f, err = os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, indexFileMode)
		}
		if err != nil {
			return err
		}
	}
	if _, err := io.Copy(f, &buf); err != nil {
		f.Close()
		_ = os.Remove(part)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, p)
}

func refuseSymlinkDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("knowledge: %s is a symlink", dir)
	}
	return nil
}

// estimate is the one token estimate the package uses.
func estimate(s string) int { return offload.Tokens(s) }
