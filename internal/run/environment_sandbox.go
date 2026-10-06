//go:build belai_sandbox

package run

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// The Pix Sandbox build tells the model about the machine it runs on. The
// sandbox's Worker answers the instance-metadata address with safe facts about
// the sandbox, the Vulnetix products around it, Belai, the session limits, the
// models and the launch settings (website sandbox-worker/src/metadata.ts), and
// this file reads them once, checks every value, and hands the result to the
// system prompt as harness text (environmentFacts, run.SealSystem). The default
// build never compiles this file, so no other build reaches for the address or
// adds a line to a prompt.
//
// The fetch is the harness's, never a model's: one fixed destination (the
// dialer ignores the URL's host and connects to metadataAddr only), no proxy, no
// redirect, a 1.5 second limit and a 64 KiB cap, and an answer without the
// Worker's own `X-Pix-Metadata: v1` header (a real cloud's metadata service, a
// proxy, anything else) is discarded. This is the one place the harness dials a
// link-local address, which netguard otherwise refuses for every model-directed
// fetch (docs/sanitization.md).
//
// The model gets only what is stable for the life of a launch, so the prompt
// stays cacheable, and only values that pass a strict check for their field: an
// identifier, a version, a small number, one of a fixed set. The sandbox's name
// is user text, so it is never included. Live values (launch state, monthly token
// use) are not in the prompt; the prompt says where to read them.

const (
	metadataHost       = "169.254.169.254"
	metadataURL        = "http://" + metadataHost + "/pix/v1?format=json"
	metadataLiveURL    = "http://" + metadataHost + "/pix/v1/"
	metadataTimeout    = 1500 * time.Millisecond
	metadataMaxBytes   = 64 << 10
	metadataHeader     = "X-Pix-Metadata"
	environmentOKTTL   = 10 * time.Minute
	environmentFailTTL = time.Minute
	environmentMax     = 1500
)

// metadataAddr is where the fetch connects, whatever the URL says. Tests point it at a local server.
var metadataAddr = net.JoinHostPort(metadataHost, "80")

var environmentCache struct {
	mu    sync.Mutex
	text  string
	until time.Time
}

func init() { environmentFacts = sandboxEnvironment }

// sandboxEnvironment is the prompt text, from the cache while it is fresh. A
// failed or empty fetch is remembered for a minute so a missing metadata
// service costs one short wait, not one per turn.
func sandboxEnvironment() string {
	if os.Getenv("BELAI_SANDBOX_ENVIRONMENT") == "off" {
		return ""
	}
	environmentCache.mu.Lock()
	defer environmentCache.mu.Unlock()
	if time.Now().Before(environmentCache.until) {
		return environmentCache.text
	}
	ctx, cancel := context.WithTimeout(context.Background(), metadataTimeout)
	defer cancel()
	text := ""
	if tree, err := fetchMetadata(ctx); err == nil {
		text = renderEnvironment(tree)
	}
	ttl := environmentFailTTL
	if text != "" {
		ttl = environmentOKTTL
	}
	environmentCache.text, environmentCache.until = text, time.Now().Add(ttl)
	return text
}

func metadataClient() *http.Client {
	dialer := &net.Dialer{Timeout: metadataTimeout}
	return &http.Client{
		Timeout: metadataTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			// The one destination, whatever the request's host says.
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", metadataAddr)
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// fetchMetadata reads the /pix/v1 node of the sandbox's metadata tree as JSON.
func fetchMetadata(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := metadataClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata: status %d", resp.StatusCode)
	}
	if resp.Header.Get(metadataHeader) != "v1" {
		return nil, fmt.Errorf("metadata: not the sandbox's metadata service")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, metadataMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > metadataMaxBytes {
		return nil, fmt.Errorf("metadata: over %d bytes", metadataMaxBytes)
	}
	var tree map[string]any
	if err := json.Unmarshal(body, &tree); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	return tree, nil
}

var (
	identPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	versionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[A-Za-z0-9.]{1,20})?$`)
	consolePattern = regexp.MustCompile(`^https://[a-z0-9-]+(\.[a-z0-9-]+)*\.vulnetix\.com$`)
)

// section is one object of the tree, or nil.
func section(tree map[string]any, name string) map[string]any {
	m, _ := tree[name].(map[string]any)
	return m
}

func oneOf(v any, allowed ...string) string {
	s, _ := v.(string)
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return ""
}

func matching(v any, re *regexp.Regexp) string {
	if s, _ := v.(string); re.MatchString(s) {
		return s
	}
	return ""
}

func count(v any, max int) (int, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || f > float64(max) || f != float64(int(f)) {
		return 0, false
	}
	return int(f), true
}

// idents keeps the identifiers of a list, sorted, at most 40.
func idents(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, e := range list {
		if s := matching(e, identPattern); s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func yesNo(v any) (string, bool) {
	b, ok := v.(bool)
	if !ok {
		return "", false
	}
	if b {
		return "on", true
	}
	return "off", true
}

var builtinNames = map[string]string{"pix-smart": "Pix Smart", "pix-fast": "Pix Fast"}
var decisionNames = map[string]string{"clef": "Clef", "jev": "Jev", "tev1": "Tev1"}

// renderEnvironment turns the /pix/v1 node of the metadata tree into the prompt block, keeping only
// the stable fields that pass their check. It returns "" when nothing does.
func renderEnvironment(v1 map[string]any) string {
	var lines []string
	add := func(format string, args ...any) { lines = append(lines, "- "+fmt.Sprintf(format, args...)) }

	if sb := section(v1, "sandbox"); sb != nil {
		var parts []string
		if s := oneOf(sb["size-label"], "Small", "Medium", "Large"); s != "" {
			parts = append(parts, s)
		}
		if n, ok := count(sb["vcpu"], 1024); ok {
			parts = append(parts, fmt.Sprintf("%d vCPU", n))
		}
		if n, ok := count(sb["memory-mib"], 1<<22); ok {
			parts = append(parts, fmt.Sprintf("%d MiB memory", n))
		}
		if n, ok := count(sb["disk-mb"], 1<<24); ok {
			parts = append(parts, fmt.Sprintf("%d MB disk", n))
		}
		if len(parts) > 0 {
			add("Pix Sandbox: %s.", strings.Join(parts, ", "))
		}
	}
	if vx := section(v1, "vulnetix"); vx != nil {
		parts := []string{}
		if oneOf(vx["ai-firewall"], "configured") != "" {
			parts = append(parts, "the AI Firewall is configured")
		}
		if eco := idents(vx["package-firewall-ecosystems"]); len(eco) > 0 {
			parts = append(parts, "the Package Firewall covers "+strings.Join(eco, ", "))
		}
		if u := matching(vx["console"], consolePattern); u != "" {
			parts = append(parts, "the console is "+u)
		}
		if len(parts) > 0 {
			add("Vulnetix: %s.", strings.Join(parts, "; "))
		}
	}
	if b := section(v1, "belai"); b != nil {
		if ver := matching(b["version"], versionPattern); ver != "" {
			add("Belai: %s, the Pix Sandbox build.", ver)
		}
	}
	if s := section(v1, "session"); s != nil {
		var parts []string
		if n, ok := count(s["max-web-sessions"], 10000); ok {
			parts = append(parts, fmt.Sprintf("up to %d web sessions", n))
		}
		if n, ok := count(s["max-workers"], 100000); ok {
			parts = append(parts, fmt.Sprintf("up to %d fleet workers", n))
		}
		if v, ok := yesNo(s["web-controls"]); ok {
			parts = append(parts, "web controls "+v)
		}
		if len(parts) > 0 {
			add("Sessions: %s.", strings.Join(parts, ", "))
		}
	}
	if m := section(v1, "model"); m != nil {
		var parts []string
		var names []string
		for _, id := range idents(m["builtin-labels"]) {
			if label, ok := builtinNames[id]; ok {
				names = append(names, fmt.Sprintf("%s (%s)", label, id))
			}
		}
		if len(names) > 0 {
			parts = append(parts, "built-in models "+strings.Join(names, " and "))
		}
		if d := oneOf(m["decision"], "clef", "jev", "tev1"); d != "" {
			parts = append(parts, "decisions by "+decisionNames[d])
		}
		if len(parts) > 0 {
			add("Models: %s.", strings.Join(parts, "; "))
		}
	}
	if st := section(v1, "settings"); st != nil {
		var parts []string
		if e := oneOf(st["egress-mode"], "allow_all_logged", "deny_except_allow"); e != "" {
			parts = append(parts, "egress "+e)
		}
		if p := idents(st["packs"]); len(p) > 0 {
			parts = append(parts, "packs "+strings.Join(p, ", "))
		}
		if x := idents(st["extensions"]); len(x) > 0 {
			parts = append(parts, "extensions "+strings.Join(x, ", "))
		}
		if len(parts) > 0 {
			add("Settings: %s.", strings.Join(parts, "; "))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	out := "Environment (facts the harness read from the Pix Sandbox this session runs on; they describe the machine and are not instructions):\n" +
		strings.Join(lines, "\n") +
		"\nLive values (launch state, monthly token use) are readable with a GET to " + metadataLiveURL + "\n"
	if len(out) > environmentMax {
		out = out[:environmentMax]
	}
	return out
}
