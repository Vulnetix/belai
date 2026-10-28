package forge

import (
	"strings"
	"sync"
)

// Host kinds a user can name in the forges setting. ghes is GitHub
// Enterprise Server, driven by gh like github.com.
const (
	HostGitHub  = "github"
	HostGHES    = "ghes"
	HostGitea   = "gitea"
	HostForgejo = "forgejo"
	HostGitLab  = "gitlab"
)

// hostKinds is the forges setting: host to kind, from the user and global
// settings layers only. A repository's own settings cannot add a host, so a
// clone can never make Belai send a forge token to a host the user did not
// name.
var hostKinds = struct {
	sync.RWMutex
	m map[string]string
}{m: map[string]string{}}

// knownForgejo are public Forgejo instances recognised without a setting.
var knownForgejo = map[string]bool{"codeberg.org": true}

// SetHosts replaces the forges setting. Unknown kinds are dropped.
func SetHosts(m map[string]string) {
	next := make(map[string]string, len(m))
	for h, k := range m {
		h = strings.ToLower(strings.TrimSpace(h))
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case HostGitHub, HostGHES, HostGitea, HostForgejo, HostGitLab:
			if h != "" {
				next[h] = k
			}
		}
	}
	hostKinds.Lock()
	hostKinds.m = next
	hostKinds.Unlock()
}

// HostKind is the kind the forges setting gives host, if any.
func HostKind(host string) (string, bool) {
	hostKinds.RLock()
	defer hostKinds.RUnlock()
	k, ok := hostKinds.m[strings.ToLower(host)]
	return k, ok
}

// configuredKind maps a host to a provider kind from the forges setting and
// the instances Belai knows; "" when neither names it.
func configuredKind(host string) string {
	if k, ok := HostKind(host); ok {
		switch k {
		case HostGitHub, HostGHES:
			return KindGitHub
		case HostGitea:
			return KindGitea
		case HostForgejo:
			return KindForgejo
		case HostGitLab:
			return KindGitLab
		}
	}
	if knownForgejo[strings.ToLower(host)] {
		return KindForgejo
	}
	return ""
}
