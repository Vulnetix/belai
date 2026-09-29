package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestWaveRuleShape(t *testing.T) {
	if WaveRule(0, 3) != "" || WaveRule(-2, 0) != "" {
		t.Fatal("a rule of no cells is not empty")
	}
	a := WaveRule(40, 0)
	if n := len([]rune(a)); n != 40 {
		t.Fatalf("WaveRule(40, 0) has %d cells", n)
	}
	if WaveRule(40, 0) != a {
		t.Fatal("the same phase drew a different rule")
	}
	if WaveRule(40, 1) == a || WaveRule(40, len(waveGlyphs)) != a {
		t.Fatal("the phase does not advance the ripple by one cell and repeat after a full period")
	}
	if WaveRule(40, -3) != WaveRule(40, len(waveGlyphs)-3) {
		t.Fatal("a negative phase is not the same ripple")
	}
	if strings.Count(a, "─") == len([]rune(a)) {
		t.Fatal("the ripple is a flat line")
	}
}

func TestPanelWaveKeepsItsWidthAndOnlyChangesTheRules(t *testing.T) {
	plain := Panel{Title: "ask", Meta: "hint", Body: "hello", Width: 60, Raw: true}
	wave := plain
	wave.Wave, wave.WavePhase = true, 2
	pl := strings.Split(ansi.Strip(plain.View()), "\n")
	wl := strings.Split(ansi.Strip(wave.View()), "\n")
	if len(pl) != len(wl) {
		t.Fatalf("%d lines with the wave, %d without", len(wl), len(pl))
	}
	for i := range pl {
		if visibleLen(pl[i]) != visibleLen(wl[i]) {
			t.Fatalf("line %d is %d cells with the wave, %d without", i, visibleLen(wl[i]), visibleLen(pl[i]))
		}
	}
	if pl[0] == wl[0] || pl[len(pl)-1] == wl[len(wl)-1] {
		t.Fatal("the wave changed neither rule")
	}
	if pl[1] != wl[1] {
		t.Fatalf("the wave changed the body row: %q vs %q", wl[1], pl[1])
	}
	if !strings.HasPrefix(wl[0], "╭─ ask") || !strings.HasSuffix(wl[0], "─╮") {
		t.Fatalf("the wave broke the frame corners or title: %q", wl[0])
	}
	if !strings.ContainsAny(wl[0], "⎽⎼⎻⎺") || !strings.ContainsAny(wl[len(wl)-1], "⎽⎼⎻⎺") {
		t.Fatal("the top and bottom rules are not both rippling")
	}
	// Off is the default and draws no wave glyph anywhere.
	if strings.ContainsAny(plain.View(), "⎽⎼⎻⎺") {
		t.Fatal("a panel without Wave drew a wave")
	}
}

func TestDictatedTurnShowsItsRawTranscript(t *testing.T) {
	m := Message{Role: "user", Content: "Add a retry to the fetch.", Voice: true, VoiceRaw: "um add a a retry to the fetch"}
	folded, _ := turnPanel(m, 80, false)
	plain := ansi.Strip(folded)
	if !strings.Contains(plain, "ctrl+o raw") {
		t.Fatalf("a folded dictated turn does not say the raw text is there: %q", plain)
	}
	if strings.Contains(plain, "um add a a retry") {
		t.Fatal("the raw transcript shows before ctrl+o")
	}
	open, _ := turnPanel(m, 80, true)
	if !strings.Contains(ansi.Strip(open), "raw · um add a a retry to the fetch") {
		t.Fatalf("ctrl+o does not show the raw transcript: %q", ansi.Strip(open))
	}
	one := m
	one.Expanded = true
	shown, _ := turnPanel(one, 80, false)
	if !strings.Contains(ansi.Strip(shown), "raw · um add") {
		t.Fatal("an expanded message does not show its raw transcript")
	}
}

func TestDictatedTurnWithUnchangedTextHasNoRawLine(t *testing.T) {
	m := Message{Role: "user", Content: "run the tests", Voice: true, VoiceRaw: "run the tests"}
	s, _ := turnPanel(m, 80, true)
	if strings.Contains(ansi.Strip(s), "raw ·") || strings.Contains(ansi.Strip(s), "ctrl+o raw") {
		t.Fatalf("a transcript the model left alone shows a raw line: %q", ansi.Strip(s))
	}
	typed := Message{Role: "user", Content: "hello", VoiceRaw: "stale"}
	s, _ = turnPanel(typed, 80, true)
	if strings.Contains(ansi.Strip(s), "raw ·") {
		t.Fatal("a typed prompt shows a raw transcript")
	}
}

func TestVoiceRawHint(t *testing.T) {
	if voiceRawHint("") != "ctrl+o raw" || voiceRawHint("12 tok") != " · ctrl+o raw" {
		t.Fatal("voiceRawHint joins wrongly")
	}
}

func TestYouColoursDifferByOrigin(t *testing.T) {
	if ColorYou == ColorVoice || ColorYou == ColorTealSoft {
		t.Fatal("typed and dictated turns must not share a colour with each other or the old teal")
	}
}
