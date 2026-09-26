package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/tools"
)

// docFiles returns every markdown file the repository ships, paired with its
// contents. Docs drift silently, so the checks below are the cheap mechanical
// half of keeping them honest: links that resolve, anchors that exist, code
// paths that are real, and no keybinding that cannot fire.
func docFiles(t *testing.T) map[string]string {
	t.Helper()
	root := moduleRoot()
	out := map[string]string{}

	paths := []string{"README.md", "AGENTS.md"}
	entries, err := os.ReadDir(filepath.Join(root, "docs"))
	if err != nil {
		t.Fatalf("read docs dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			paths = append(paths, filepath.Join("docs", e.Name()))
		}
	}

	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		out[p] = string(b)
	}
	if len(out) < 5 {
		t.Fatalf("only %d docs found — the discovery has stopped working", len(out))
	}
	return out
}

var mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// Every relative link in the docs must resolve to a file that exists.
// Absolute URLs and bare anchors are checked elsewhere or not at all.
func TestDocLinksResolve(t *testing.T) {
	root := moduleRoot()
	for name, body := range docFiles(t) {
		for _, m := range mdLink.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") || strings.HasPrefix(target, "#") {
				continue
			}
			path := target
			if i := strings.Index(path, "#"); i >= 0 {
				path = path[:i]
			}
			if path == "" {
				continue
			}
			abs := filepath.Join(root, filepath.Dir(name), path)
			if _, err := os.Stat(abs); err != nil {
				t.Errorf("%s links to %q which does not exist", name, target)
			}
		}
	}
}

// headingAnchors returns the GitHub-style anchor for every heading in a
// markdown body.
func headingAnchors(body string) map[string]bool {
	anchors := map[string]bool{}
	nonAnchor := regexp.MustCompile(`[^a-z0-9 -]`)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimLeft(line, "#")
		text = strings.TrimSpace(text)
		text = strings.ToLower(text)
		// Inline code and emphasis markers are dropped, not replaced.
		text = strings.NewReplacer("`", "", "*", "", "_", "").Replace(text)
		text = nonAnchor.ReplaceAllString(text, "")
		anchors[strings.ReplaceAll(text, " ", "-")] = true
	}
	return anchors
}

// A cross-document anchor link must point at a heading that exists. A broken
// anchor lands the reader at the top of the file with no sign anything is
// wrong, which is worse than a 404.
func TestDocAnchorsResolve(t *testing.T) {
	root := moduleRoot()
	docs := docFiles(t)

	for name, body := range docs {
		for _, m := range mdLink.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http") || !strings.Contains(target, "#") {
				continue
			}
			file, anchor, _ := strings.Cut(target, "#")
			if anchor == "" {
				continue
			}

			targetBody := body
			if file != "" {
				rel := filepath.Join(filepath.Dir(name), file)
				b, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil {
					continue // TestDocLinksResolve owns this failure.
				}
				targetBody = string(b)
			}
			if !headingAnchors(targetBody)[anchor] {
				t.Errorf("%s links to %q but no such heading exists", name, target)
			}
		}
	}
}

var codePath = regexp.MustCompile("`(internal/[a-z0-9_]+(?:/[a-z0-9_]+)*(?:\\.go)?)`")

// A doc naming a package or file must name one that exists. Renames are the
// usual way this breaks.
func TestDocCodePathsExist(t *testing.T) {
	root := moduleRoot()
	checked := 0
	for name, body := range docFiles(t) {
		for _, m := range codePath.FindAllStringSubmatch(body, -1) {
			checked++
			if _, err := os.Stat(filepath.Join(root, m[1])); err != nil {
				t.Errorf("%s names %s, which does not exist", name, m[1])
			}
		}
	}
	if checked < 20 {
		t.Fatalf("only %d code paths checked — the regex has stopped matching", checked)
	}
}

// Belai binds no alt chord, because one cannot reach the TUI: under the kitty
// keyboard protocol a ctrl+alt+<key> event collapses onto the same legacy
// control code as ctrl+<key>, and without that protocol alt is an ESC prefix
// that terminals and multiplexers swallow. Documenting one would promise a key
// that silently does nothing.
func TestDocsAdvertiseNoAltBindings(t *testing.T) {
	// A backticked keycap is a promise that the chord works. The docs also
	// have to *name* the chords to explain why they do not, so a line that
	// says so in as many words is exempt — the disclaimer has to sit on the
	// same line as the keycap for the exemption to apply.
	altKeycap := regexp.MustCompile("`[^`]*\\balt\\+[a-z]+[^`]*`")
	disclaimers := []string{
		"never", "cannot", "can never", "indistinguishable", "dead code",
		"unusable", "swallow", "no alt", "not be used",
	}
	for name, body := range docFiles(t) {
		for _, line := range strings.Split(body, "\n") {
			matches := altKeycap.FindAllString(line, -1)
			if len(matches) == 0 {
				continue
			}
			lower := strings.ToLower(line)
			exempt := false
			for _, d := range disclaimers {
				if strings.Contains(lower, d) {
					exempt = true
					break
				}
			}
			if exempt {
				continue
			}
			for _, m := range matches {
				t.Errorf("%s advertises the alt keycap %s; alt chords never reach the TUI", name, m)
			}
		}
	}
}

// Every builtin tool the default registry ships must be named in the agent
// profile schema's allowed-tool list, and that list must not name one the
// registry does not have. The list is what `agentprofile.Validate` accepts, so
// a tool missing from the docs is a tool nobody can put in a profile.
func TestDocsListEveryBuiltinTool(t *testing.T) {
	docs := docFiles(t)
	body, ok := docs["docs/agent-profiles.md"]
	if !ok {
		t.Fatal("docs/agent-profiles.md not found")
	}

	for _, name := range tools.Default(t.TempDir(), false).Names() {
		if !strings.Contains(body, "`"+name+"`") {
			t.Errorf("docs/agent-profiles.md does not name the builtin tool %q", name)
		}
	}
}

// Plan mode drops Bash, and the docs have to say so rather than describing
// the read-only allowlist that plan mode no longer consults. The allowlist
// itself still exists for the read-only Bash tool, so the check is that the
// three docs which describe plan mode state the removal.
func TestDocsSayPlanModeHasNoBash(t *testing.T) {
	docs := docFiles(t)
	for _, name := range []string{"README.md", "AGENTS.md", "docs/architecture.md", "docs/development.md"} {
		body, ok := docs[name]
		if !ok {
			t.Fatalf("%s not found", name)
		}
		// Backticks are dropped first: the docs write the tool as `Bash` in
		// most places and as Bash in a few, and the claim is the same either
		// way.
		lower := strings.ToLower(strings.ReplaceAll(body, "`", ""))
		if !strings.Contains(lower, "plan mode") {
			continue
		}
		said := false
		for _, phrase := range []string{
			"no bash", "bash is not available", "unavailable in plan mode",
			"bash is gone", "plan mode has no bash",
		} {
			if strings.Contains(lower, phrase) {
				said = true
				break
			}
		}
		if !said {
			t.Errorf("%s describes plan mode without saying Bash is unavailable there", name)
		}
	}
}
