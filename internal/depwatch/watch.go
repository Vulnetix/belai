package depwatch

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Profile names of the per-ecosystem background agents. Each is a built-in
// agentprofile (internal/agentprofile/builtin/deps-*.json) that knows its
// ecosystem's lockfile mechanics, transitive coercion and manifest quirks.
const (
	ProfileJavaScript = "signet:deps-javascript"
	ProfilePython     = "signet:deps-python"
	ProfileGo         = "signet:deps-go"
	ProfileRust       = "signet:deps-rust"
	ProfileRuby       = "signet:deps-ruby"
	ProfileJVM        = "signet:deps-jvm"
	ProfileDotNet     = "signet:deps-dotnet"
	ProfilePHP        = "signet:deps-php"
	ProfileApple      = "signet:deps-apple"
	ProfileContainers = "signet:deps-containers"
	ProfileCI         = "signet:deps-ci"
	ProfileOther      = "signet:deps-other"
)

// Profiles lists every ecosystem profile, for tests and docs.
var Profiles = []string{
	ProfileJavaScript, ProfilePython, ProfileGo, ProfileRust, ProfileRuby, ProfileJVM,
	ProfileDotNet, ProfilePHP, ProfileApple, ProfileContainers, ProfileCI, ProfileOther,
}

// ProfileFor maps a CLI ecosystem to the background-agent profile that knows
// how to address its manifests. Every ecosystem has one: the niche ones share
// signet:deps-other.
func ProfileFor(ecosystem string) string {
	switch ecosystem {
	case "npm", "deno":
		return ProfileJavaScript
	case "pypi", "conda":
		return ProfilePython
	case "golang":
		return ProfileGo
	case "cargo":
		return ProfileRust
	case "rubygems":
		return ProfileRuby
	case "maven", "clojars":
		return ProfileJVM
	case "nuget":
		return ProfileDotNet
	case "composer":
		return ProfilePHP
	case "swift", "cocoapods", "carthage":
		return ProfileApple
	case "docker", "kubernetes", "helm", "terraform":
		return ProfileContainers
	case "ci", "github-actions", "shell":
		return ProfileCI
	}
	return ProfileOther
}

// Change is one manifest the session changed during a turn: its content
// before the turn's first change and after its last.
type Change struct {
	Path    string // workdir-relative, slash-separated
	Info    Info
	Old     string
	New     string
	Created bool
	// Truncated means the recorder could not diff the file (too large or
	// binary): the digest is unknown and the change is checked without asking
	// the fast model.
	Truncated bool
}

// Coalescer collects manifest changes for one turn. A manifest edited five
// times in a turn is checked once, against the turn's net change, and a file
// whose net change is nothing is not checked at all. It is safe for
// concurrent use.
type Coalescer struct {
	mu      sync.Mutex
	pending map[string]*Change
}

// Observe records one file change. It reports whether the path is a manifest.
// Deleted files are ignored: removing a manifest adds no dependency.
func (c *Coalescer) Observe(relPath, old, new string, created, deleted, truncated bool) bool {
	if deleted {
		c.mu.Lock()
		delete(c.pending, relPath)
		c.mu.Unlock()
		return false
	}
	info, ok := Detect(relPath, new)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		c.pending = map[string]*Change{}
	}
	if prev, ok := c.pending[relPath]; ok {
		prev.New = new
		prev.Info = info
		prev.Truncated = prev.Truncated || truncated
		return true
	}
	c.pending[relPath] = &Change{Path: relPath, Info: info, Old: old, New: new, Created: created, Truncated: truncated}
	return true
}

// Drain returns the turn's net manifest changes, sorted by path, and resets
// the coalescer.
func (c *Coalescer) Drain() []Change {
	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	out := make([]Change, 0, len(pending))
	for _, ch := range pending {
		if !ch.Truncated && ch.Old == ch.New {
			continue // edited and restored within the turn
		}
		out = append(out, *ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Digest bounds.
const (
	maxDigestLines     = 120
	maxDigestLineRunes = 240
)

// Digest renders the net change as removed/added lines — a multiset
// difference, not a positional diff, which is all the dependency question
// needs and stays bounded on a regenerated lockfile. It is the only view of
// the file the fast model is given.
func Digest(ch Change) string {
	if ch.Truncated {
		return "(file too large or binary to diff)"
	}
	removed, added := lineDelta(ch.Old, ch.New)
	var b strings.Builder
	fmt.Fprintf(&b, "manifest: %s (type %s, ecosystem %s", ch.Path, ch.Info.Type, ch.Info.Ecosystem)
	if ch.Info.Lock {
		b.WriteString(", lockfile")
	}
	if ch.Created {
		b.WriteString(", new file")
	}
	fmt.Fprintf(&b, ")\n%d lines removed, %d lines added\n", len(removed), len(added))
	n := 0
	emit := func(prefix string, lines []string) {
		for _, l := range lines {
			if n >= maxDigestLines {
				return
			}
			if r := []rune(l); len(r) > maxDigestLineRunes {
				l = string(r[:maxDigestLineRunes]) + "…"
			}
			b.WriteString(prefix + l + "\n")
			n++
		}
	}
	emit("- ", removed)
	emit("+ ", added)
	if total := len(removed) + len(added); total > n {
		fmt.Fprintf(&b, "… %d more changed lines not shown\n", total-n)
	}
	return b.String()
}

// lineDelta returns the non-blank lines only in old and only in new, as
// multisets, each in its file order.
func lineDelta(old, new string) (removed, added []string) {
	count := map[string]int{}
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			count[l]++
		}
	}
	for _, l := range strings.Split(new, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if count[l] > 0 {
			count[l]--
			continue
		}
		added = append(added, l)
	}
	for _, l := range strings.Split(old, "\n") {
		if count[l] > 0 {
			count[l]--
			removed = append(removed, l)
		}
	}
	return removed, added
}
