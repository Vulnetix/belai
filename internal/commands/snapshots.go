package commands

import (
	"net/url"
	"regexp"
	"strings"
	"sync"
)

// Snapshot is a link the Vulnetix CLI printed to a scan's results on the
// website. The CLI prints them only for an authenticated organisation (never
// for the shared community credentials).
type Snapshot struct {
	Label string // "SCA", "Malscan", "License", …; "" when the line had none
	URL   string
}

// snapshotLine matches every form the CLI prints:
//
//	Snapshot: <url>
//	  ✓ Snapshot: <url>
//	  ✓ SAST Snapshot: <url>
//	SAST snapshot: <url>
//	Malscan snapshot: <url>
var snapshotLine = regexp.MustCompile(`(?i)^\s*(?:✓\s*)?([A-Za-z][A-Za-z0-9 ./+-]*?)?\s*snapshot:\s*(https://\S+)\s*$`)

// bareURL is the binary scanner's form: the URL alone, indented, on the line
// after "API: N binaries stored …".
var bareURL = regexp.MustCompile(`^\s+(https://\S+)\s*$`)

// vulnetixLink accepts only https links on vulnetix.com, the only place a
// snapshot lives, so a scanned file's own text can never plant a link here.
func vulnetixLink(raw string) (string, bool) {
	raw = strings.TrimRight(raw, ".,;:)")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", false
	}
	h := strings.ToLower(u.Hostname())
	if h != "vulnetix.com" && !strings.HasSuffix(h, ".vulnetix.com") {
		return "", false
	}
	return raw, true
}

// snapshotCollector reads scanner output line by line and keeps the snapshot
// links it prints, in order, without duplicates. It is safe for concurrent use.
type snapshotCollector struct {
	mu    sync.Mutex
	prev  string
	seen  map[string]bool
	items []Snapshot
}

func (c *snapshotCollector) line(s string) {
	for _, l := range strings.Split(s, "\n") {
		c.one(strings.TrimRight(l, "\r"))
	}
}

func (c *snapshotCollector) one(l string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.prev
	c.prev = l
	if m := snapshotLine.FindStringSubmatch(l); m != nil {
		c.add(strings.TrimSpace(m[1]), m[2])
		return
	}
	if m := bareURL.FindStringSubmatch(l); m != nil && strings.HasPrefix(strings.TrimSpace(prev), "API:") {
		c.add("Binary", m[1])
	}
}

func (c *snapshotCollector) add(label, raw string) {
	link, ok := vulnetixLink(raw)
	if !ok {
		return
	}
	if c.seen == nil {
		c.seen = map[string]bool{}
	}
	if c.seen[link] {
		return
	}
	c.seen[link] = true
	c.items = append(c.items, Snapshot{Label: label, URL: link})
}

func (c *snapshotCollector) snapshots() []Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Snapshot(nil), c.items...)
}

// ParseSnapshots returns the snapshot links in a scanner's output.
func ParseSnapshots(output string) []Snapshot {
	var c snapshotCollector
	c.line(output)
	return c.snapshots()
}
