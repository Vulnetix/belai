package acp

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestACPPageHasARowForEveryMethod keeps the What is supported table equal to
// the methods the server answers, in both directions.
func TestACPPageHasARowForEveryMethod(t *testing.T) {
	doc := docparity.Read(t, "docs/acp.md")
	for _, m := range Methods {
		if !strings.Contains(doc, "\n| `"+m+"` |") {
			t.Errorf("docs/acp.md has no table row for %s", m)
		}
	}
	// session/request_permission is a request Belai sends to the editor, not a
	// method it answers.
	listed := map[string]bool{"session/request_permission": true}
	for _, m := range Methods {
		listed[m] = true
	}
	for _, m := range regexp.MustCompile("(?m)^\\| `(session/[a-z_]+|initialize|authenticate)` \\|").FindAllStringSubmatch(doc, -1) {
		if !listed[m[1]] {
			t.Errorf("docs/acp.md documents %s, which the server does not answer", m[1])
		}
	}
}

// TestACPPageStatesTheCapabilitiesAndLimits pins the protocol version, the
// capabilities initialize advertises, the modes, the heartbeat and the image
// cap the page states.
func TestACPPageStatesTheCapabilitiesAndLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/acp.md")), " ")
	for _, want := range []string{
		"protocol version 1",
		"embedded file context and images accepted, audio not",
		"advertises `sessionCapabilities` `list` and `close`",
		"(`http` and `sse` are both false)",
		"`loadSession` is false",
		"`auto` (Belai chooses per prompt, the default), `agent`, `plan` (read only, no shell) or `goal`",
		"\"Still working\" after ten quiet seconds",
		"**At most eight images per prompt**",
		"`(image attached)`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/acp.md does not say %q", want)
		}
	}
	if ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %v, the page says 1", ProtocolVersion)
	}
	if heartbeatEvery != 10*time.Second {
		t.Errorf("heartbeatEvery = %v, the page says ten seconds", heartbeatEvery)
	}
	if maxPromptImages != 8 {
		t.Errorf("maxPromptImages = %d, the page says eight", maxPromptImages)
	}
	var ids []string
	for _, m := range modeList {
		ids = append(ids, m["id"].(string))
	}
	if strings.Join(ids, ",") != "auto,agent,plan,goal,code" {
		t.Errorf("modes %v, the page lists auto, agent, plan, goal and code", ids)
	}
}

// TestACPPageNamesEveryToolKind keeps the tool kinds the page lists a superset
// of the kinds the server can send for a tool call.
func TestACPPageNamesEveryToolKind(t *testing.T) {
	doc := docparity.Read(t, "docs/acp.md")
	for _, tool := range []string{"Read", "Write", "Edit", "Grep", "Bash", "WebFetch", "update_plan", "mcp__x__y"} {
		kind := toolKind(tool)
		if kind == "" || !strings.Contains(doc, "`"+kind+"`") {
			t.Errorf("toolKind(%s) = %q, which docs/acp.md does not list", tool, kind)
		}
	}
}

// TestEditorPagesAgreeWithTheACPPage keeps the five editor guides consistent
// with docs/acp.md: each gives the same command, links back to it, and none
// repeats a limitation the main page no longer has.
func TestEditorPagesAgreeWithTheACPPage(t *testing.T) {
	for _, name := range []string{"zed", "jetbrains", "neovim", "emacs", "vscode"} {
		doc := docparity.Read(t, "docs/acp-"+name+".md")
		if !strings.Contains(doc, "belai acp") && !strings.Contains(doc, `"acp"`) {
			t.Errorf("acp-%s.md does not show the belai acp command", name)
		}
		if !strings.Contains(doc, "acp.md#") {
			t.Errorf("acp-%s.md does not link to the sections of acp.md", name)
		}
		for _, stale := range []string{"not in the Belai session store", "images and audio are not accepted"} {
			if strings.Contains(strings.Join(strings.Fields(doc), " "), stale) {
				t.Errorf("acp-%s.md says %q, which acp.md contradicts", name, stale)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(doc), " "), "audio is not accepted (images are)") {
			t.Errorf("acp-%s.md does not state the image and audio support", name)
		}
	}
	main := docparity.Read(t, "docs/acp.md")
	for _, name := range []string{"zed", "jetbrains", "neovim", "emacs", "vscode"} {
		if !strings.Contains(main, "acp-"+name+".md") {
			t.Errorf("acp.md does not link to acp-%s.md", name)
		}
	}
}
