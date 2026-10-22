package agentprofile

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
)

// List runs on every Auto turn (the agent pick), so the user profiles it
// parsed are kept until a profile file changes. The key is every profile
// file's name, size and modification time, not the directory's: editing a
// profile in place leaves the directory's time alone. A hit decodes fresh
// copies from stored JSON, so no caller shares a pointer, slice or map with
// another.
var listCache struct {
	mu  sync.Mutex
	key string
	raw [][]byte
	// The fields JSON does not carry.
	file      []string
	builtin   []bool
	ephemeral []bool
}

// profilesKey fingerprints the profile files of a directory listing. keyed is
// false when a file's details cannot be read, and List then parses as before.
func profilesKey(dir string, entries []os.DirEntry) (string, bool) {
	var b strings.Builder
	b.WriteString(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return "", false
		}
		b.WriteString("\x00" + e.Name() + "\x00" + strconv.FormatInt(info.Size(), 10) + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10))
	}
	return b.String(), true
}

// cachedProfiles returns fresh copies of the profiles stored under key.
func cachedProfiles(key string, keyed bool) ([]AgentProfile, bool) {
	if !keyed {
		return nil, false
	}
	listCache.mu.Lock()
	defer listCache.mu.Unlock()
	if listCache.key != key || listCache.raw == nil {
		return nil, false
	}
	out := make([]AgentProfile, len(listCache.raw))
	for i, raw := range listCache.raw {
		if err := json.Unmarshal(raw, &out[i]); err != nil {
			return nil, false
		}
		out[i].File, out[i].Builtin, out[i].Ephemeral = listCache.file[i], listCache.builtin[i], listCache.ephemeral[i]
	}
	return out, true
}

// storeProfiles keeps the parsed profiles under key.
func storeProfiles(key string, ps []AgentProfile) {
	raw := make([][]byte, len(ps))
	file := make([]string, len(ps))
	builtin := make([]bool, len(ps))
	ephemeral := make([]bool, len(ps))
	for i, p := range ps {
		b, err := json.Marshal(p)
		if err != nil {
			return
		}
		raw[i], file[i], builtin[i], ephemeral[i] = b, p.File, p.Builtin, p.Ephemeral
	}
	listCache.mu.Lock()
	listCache.key, listCache.raw, listCache.file, listCache.builtin, listCache.ephemeral = key, raw, file, builtin, ephemeral
	listCache.mu.Unlock()
}
