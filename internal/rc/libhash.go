package rc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// The hash the website compares for an agent is the hash of the markdown the
// library stores for a version. A version a person wrote in the console is stored
// as the console wrote it, and Belai renders a profile back in its own key order
// and JSON escaping, so the render of an installed, unedited agent is not the
// library's bytes and its hash is no version's hash. At install the host keeps
// the library's hash beside the hash of what it rendered. While the profile still
// renders the same, the inventory and the sync report the library's hash; once it
// is edited they report the hash of what is there, which is then an edit. A
// profile with no record (a hand-written one, one backed up from this host, one
// installed by an older Belai) reports its render, whose bytes are what a push
// stores. Crews need none: the library stores a crew in Belai's canonical JSON.

const libHashFile = "library-hashes.json"

// maxLibHashes bounds the file; a host holds far fewer installed profiles.
const maxLibHashes = 4096

type libHashEntry struct {
	// Render is the hash of what this host rendered for the profile at install.
	Render string `json:"render"`
	// Library is the hash of the exact bytes the library holds for that version.
	Library string `json:"library"`
}

var libHashMu sync.Mutex

func libHashPath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, libHashFile), nil
}

// readLibraryHashes reads the record, by profile id. A missing or unreadable file is empty.
func readLibraryHashes() map[string]libHashEntry {
	libHashMu.Lock()
	defer libHashMu.Unlock()
	return readLibHashesLocked()
}

func readLibHashesLocked() map[string]libHashEntry {
	out := map[string]libHashEntry{}
	p, err := libHashPath()
	if err != nil {
		return out
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return out
	}
	if json.Unmarshal(data, &out) != nil || out == nil {
		return map[string]libHashEntry{}
	}
	return out
}

// recordLibraryHash is the installer's Fetched callback: for the profile id it
// keeps the library's hash for the installed version beside the hash of this
// host's render of it. Equal hashes need no record, and an old one is dropped.
// A write that fails leaves the profile reporting its render, which is only a
// less exact answer, so it is not an error.
func recordLibraryHash(kind, id string, library, rendered []byte) {
	if kind != "agent" {
		return
	}
	libHashMu.Lock()
	defer libHashMu.Unlock()
	m := readLibHashesLocked()
	lib, ren := hashOf(library), hashOf(rendered)
	if lib == ren {
		delete(m, id)
	} else if _, had := m[id]; had || len(m) < maxLibHashes {
		m[id] = libHashEntry{Render: ren, Library: lib}
	} else {
		return
	}
	p, err := libHashPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(m)
	if err != nil || os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	if os.Rename(tmp, p) != nil {
		_ = os.Remove(tmp)
	}
}

// hashFor is the hash to report for an agent whose render hashes to render: the
// library's hash when it is still the install's render, otherwise the render's.
func hashFor(held map[string]libHashEntry, id, render string) string {
	if e, ok := held[id]; ok && e.Render == render && e.Library != "" {
		return e.Library
	}
	return render
}
