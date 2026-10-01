package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// TestVoicePageStatesTheComposerLimits pins the tap window, the held text and
// queue bounds, the wake word window, the spoken instruction word limit and the
// default score docs/voice.md gives.
func TestVoicePageStatesTheComposerLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/voice.md")), " ")
	for _, want := range []string{
		"If no repeat follows within 0.7 seconds it was a tap",
		"up to 4,000 characters",
		"at most eight wait",
		"arms the next utterance for eight seconds",
		"A short utterance (up to 24 words)",
		"`jev.thresholds.voice_at` (0.95 by default)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/voice.md does not say %q", want)
		}
	}
	if voiceTapGrace != 700*time.Millisecond || voiceHeldMax != 4000 || voiceQueueMax != 8 {
		t.Errorf("tap grace %v, held max %d, queue max %d disagree with the page", voiceTapGrace, voiceHeldMax, voiceQueueMax)
	}
	if voiceWakeWindow != 8*time.Second || voiceCommandMaxWords != 24 {
		t.Errorf("wake window %v and %d words disagree with the page", voiceWakeWindow, voiceCommandMaxWords)
	}
	if got := config.DefaultJevThresholds().VoiceAt; got != 0.95 {
		t.Errorf("the default voice_at is %v, the page says 0.95", got)
	}
}

// TestVoicePageListsEveryVoiceCommand derives the verbs from the usage line the
// handler prints for an unknown one, so a verb added without a Commands row
// fails here.
func TestVoicePageListsEveryVoiceCommand(t *testing.T) {
	src := docparity.Read(t, "internal/tui/voice.go")
	usage := regexp.MustCompile(`usage: /voice \[([^\]]+)\]`).FindStringSubmatch(src)
	if usage == nil {
		t.Fatal("the /voice usage line moved")
	}
	doc := docparity.Read(t, "docs/voice.md")
	section := regexp.MustCompile(`(?s)## Commands(.*?)## Limitations`).FindStringSubmatch(doc)
	if section == nil {
		t.Fatal("the Commands section moved")
	}
	verbs := strings.Split(usage[1], "|")
	if len(verbs) < 12 {
		t.Fatalf("only %d verbs found in the usage line: %v", len(verbs), verbs)
	}
	for _, v := range verbs {
		word := strings.Fields(v)[0]
		if !strings.Contains(section[1], "`/voice "+word) && !(word == "status" && strings.Contains(section[1], "`/voice` or `/voice status`")) {
			t.Errorf("the Commands table has no row for /voice %s", word)
		}
	}
}
