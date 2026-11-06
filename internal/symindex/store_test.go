package symindex

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	root := fixture(t)
	ix := build(t, root, nil, Limits{})
	data, err := ix.Encode()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(data, ix.Root(), 2000)
	if err != nil {
		t.Fatal(err)
	}
	if back.Stats() != ix.Stats() {
		t.Errorf("stats %+v != %+v", back.Stats(), ix.Stats())
	}
	a, _ := ix.Outline("srv/server.go")
	b, _ := back.Outline("srv/server.go")
	if !reflect.DeepEqual(a, b) {
		t.Errorf("outline %+v != %+v", a, b)
	}
}

// reseal recomputes the checksum so a test can hand Decode a well-formed file
// that holds a bad row.
func reseal(body []byte) []byte {
	sum := sha256.Sum256(body)
	return append(append([]byte{}, body...), sum[:]...)
}

func TestDecodeRefusesAnythingItCannotTrust(t *testing.T) {
	root := fixture(t)
	ix := build(t, root, nil, Limits{})
	good, _ := ix.Encode()
	body := good[:len(good)-sha256.Size]

	flip := append([]byte{}, good...)
	flip[len(cacheMagic)+20] ^= 0x01
	cases := map[string][]byte{
		"empty":            nil,
		"short":            []byte("belai-symindex 1\n"),
		"wrong magic":      reseal(bytes.Replace(body, []byte("belai-symindex 1"), []byte("belai-symindex 2"), 1)),
		"flipped byte":     flip,
		"truncated":        good[:len(good)-5],
		"trailing garbage": reseal(append(append([]byte{}, body...), []byte(" {}")...)),
		"unknown field":    reseal(bytes.Replace(body, []byte(`"entries"`), []byte(`"extra":1,"entries"`), 1)),
		"control in name":  reseal(bytes.Replace(body, []byte(`"n":"Server"`), []byte(`"n":"Ser\u0000ver"`), 1)),
		"bad kind":         reseal(bytes.Replace(body, []byte(`"k":"struct"`), []byte(`"k":"<tools>"`), 1)),
		"path traversal":   reseal(bytes.Replace(body, []byte(`"p":"srv/server.go"`), []byte(`"p":"../srv/server.go"`), 1)),
		"sensitive path":   reseal(bytes.Replace(body, []byte(`"p":"srv/server.go"`), []byte(`"p":"srv/id_rsa.go"`), 1)),
		"absolute path":    reseal(bytes.Replace(body, []byte(`"p":"srv/server.go"`), []byte(`"p":"/etc/passwd.go"`), 1)),
		"duplicate path":   reseal(bytes.Replace(body, []byte(`"p":"srv/server.go"`), []byte(`"p":"srv/client.go"`), 1)),
		"line zero":        reseal(bytes.Replace(body, []byte(`"l":3`), []byte(`"l":0`), 1)),
	}
	for name, data := range cases {
		if _, err := Decode(data, ix.Root(), 2000); !errors.Is(err, ErrCache) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := Decode(good, "/other/root", 2000); !errors.Is(err, ErrCache) {
		t.Errorf("another root accepted: %v", err)
	}
	if _, err := Decode(good, ix.Root(), 1000); !errors.Is(err, ErrCache) {
		t.Errorf("a changed max_files accepted: %v", err)
	}
}

func TestDecodeRefusesMoreRowsThanTheBounds(t *testing.T) {
	ix := &Index{root: "/r", entries: map[string]*Entry{}, maxFiles: 100, eligible: 100}
	e := &Entry{Path: "a.go"}
	for i := 0; i < MaxPerFile+1; i++ {
		e.Symbols = append(e.Symbols, Symbol{Name: "A", Line: i + 1})
	}
	ix.entries["a.go"] = e
	data, _ := ix.Encode()
	if _, err := Decode(data, "/r", 100); !errors.Is(err, ErrCache) {
		t.Fatalf("an oversized file entry was accepted: %v", err)
	}
}

func TestSaveAndLoadUseAPrivateFile(t *testing.T) {
	root := fixture(t)
	ix := build(t, root, nil, Limits{})
	dir := filepath.Join(t.TempDir(), "symbols")
	p := filepath.Join(dir, "k.idx")
	if err := ix.Save(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info, err)
	}
	if _, err := os.Stat(p + ".lock"); !os.IsNotExist(err) {
		t.Error("the lock file was left behind")
	}
	back, err := Load(p, ix.Root(), 2000)
	if err != nil || back.Stats() != ix.Stats() {
		t.Fatalf("load: %v", err)
	}
	// A corrupt file is refused and left alone.
	if err := os.WriteFile(p, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p, ix.Root(), 2000); err == nil {
		t.Error("a corrupt file loaded")
	}
	if _, err := Load(filepath.Join(dir, "missing.idx"), ix.Root(), 2000); err == nil {
		t.Error("a missing file loaded")
	}
}

func TestSaveRefusesASymlinkedTarget(t *testing.T) {
	root := fixture(t)
	ix := build(t, root, nil, Limits{})
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "k.idx")
	if err := os.Symlink(victim, p); err != nil {
		t.Skip("no symlinks")
	}
	if err := ix.Save(p); err == nil {
		t.Error("Save wrote through a symlink")
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Error("the symlink target was overwritten")
	}
	if _, err := Load(p, ix.Root(), 2000); err == nil {
		t.Error("Load followed a symlink")
	}
	// A symlinked directory is refused too.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "d")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks")
	}
	if err := ix.Save(filepath.Join(link, "k.idx")); err == nil {
		t.Error("Save wrote into a symlinked directory")
	}
}
