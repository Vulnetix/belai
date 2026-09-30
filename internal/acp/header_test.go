package acp

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/version"
)

func TestHeaderText(t *testing.T) {
	oldV, oldC := version.Version, version.Commit
	version.Version, version.Commit = "v1.2.3", "abcdef123456789"
	defer func() { version.Version, version.Commit = oldV, oldC }()
	h := headerText("cloudflare-workers-ai", "@cf/deepseek\x1b[31m")
	for _, want := range []string{"**belai**", "v1.2.3", "commit abcdef123456", "cloudflare-workers-ai · @cf/deepseek"} {
		if !strings.Contains(h, want) {
			t.Errorf("header lacks %q: %q", want, h)
		}
	}
	if strings.Contains(h, "\x1b") {
		t.Fatalf("control rune in header: %q", h)
	}
}

// The header goes out once, at the start of a session's first turn.
func TestHeaderIsSentOnce(t *testing.T) {
	s, ed := postEndServer(t, nil)
	ss := &acpSession{id: "a", always: map[string]bool{}}
	s.sendHeader(ss)
	s.sendHeader(ss)
	got := waitUpdates(t, ed, 1)
	if len(got) != 1 || got[0]["sessionUpdate"] != "agent_message_chunk" {
		t.Fatalf("updates = %v", got)
	}
}
