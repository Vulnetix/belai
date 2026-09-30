package components

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func card(state string, pos, dur time.Duration) *PlayerCard {
	return &PlayerCard{Title: "The build passed", State: state, Pos: pos, Dur: dur, Final: true, Speed: 1.25, Levels: []float64{0, 0.3, 0.9}, Status: "from cache"}
}

func renderCard(c *PlayerCard, width int) (string, LineMap) {
	return playerPanel(Message{Role: PlayerRole, Player: c}, width)
}

func TestPlayerCardHasAHitForEveryControl(t *testing.T) {
	_, lm := renderCard(card("playing", 30*time.Second, 2*time.Minute), 100)
	var got []string
	for _, sl := range lm {
		for _, h := range sl.Hits {
			if h.Width <= 0 {
				t.Errorf("hit %+v has no width", h)
			}
			got = append(got, h.Action)
		}
	}
	for _, want := range []string{PlayerBack, PlayerToggle, PlayerStop, PlayerFwd, PlayerSeek} {
		if !contains(got, want) {
			t.Errorf("no %q hit: %v", want, got)
		}
	}
	for _, sp := range PlayerSpeeds {
		if !contains(got, PlayerSpeed+strings.TrimRight(strings.TrimRight(SpeedLabel(sp), "×"), " ")) {
			t.Errorf("no chip for %v: %v", sp, got)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestPlayerCardHitsLineUpWithTheRenderedText(t *testing.T) {
	out, lm := renderCard(card("playing", 30*time.Second, 2*time.Minute), 100)
	lines := strings.Split(out, "\n")
	if len(lines) != len(lm) {
		t.Fatalf("%d lines, %d line-map rows: the invariant is broken", len(lines), len(lm))
	}
	for i, sl := range lm {
		plain := ansi.Strip(lines[i])
		for _, h := range sl.Hits {
			if h.Col < 0 || h.Col+h.Width > ansi.StringWidth(plain)+1 {
				t.Errorf("line %d: hit %+v outside %q", i, h, plain)
			}
			text := ansi.Cut(plain, h.Col, h.Col+h.Width)
			switch {
			case h.Action == PlayerToggle && !strings.Contains(text, "pause"):
				t.Errorf("toggle hit covers %q", text)
			case h.Action == PlayerStop && !strings.Contains(text, "stop"):
				t.Errorf("stop hit covers %q", text)
			case h.Action == PlayerSeek && !strings.Contains(text, "●"):
				t.Errorf("seek hit covers %q, which has no knob", text)
			}
		}
	}
}

func TestPlayerCardTransportFollowsTheState(t *testing.T) {
	cases := map[string]string{
		"playing": "pause", "paused": "play", "ended": "replay", "stopped": "replay", "buffering": "wait", "idle": "play",
	}
	for state, word := range cases {
		out, _ := renderCard(card(state, 0, time.Minute), 100)
		if !strings.Contains(ansi.Strip(out), word) {
			t.Errorf("%s: no %q in\n%s", state, word, ansi.Strip(out))
		}
	}
}

func TestPlayerCardShowsPositionDurationAndALiveMarker(t *testing.T) {
	c := card("playing", 72*time.Second, 220*time.Second)
	out := ansi.Strip(func() string { s, _ := renderCard(c, 100); return s }())
	if !strings.Contains(out, "1:12") || !strings.Contains(out, "3:40") || strings.Contains(out, "3:40+") {
		t.Fatalf("clock in\n%s", out)
	}
	c.Final = false
	out = ansi.Strip(func() string { s, _ := renderCard(c, 100); return s }())
	if !strings.Contains(out, "3:40+") {
		t.Fatalf("no + while audio is still arriving:\n%s", out)
	}
}

func TestPlayerCardSpeedChipMarksTheActiveSpeed(t *testing.T) {
	out, _ := renderCard(card("playing", 0, time.Minute), 100)
	if !strings.Contains(ansi.Strip(out), "[1.25×]") {
		t.Fatalf("active speed not bracketed:\n%s", ansi.Strip(out))
	}
	if strings.Contains(ansi.Strip(out), "[1×]") {
		t.Fatal("two speeds look active")
	}
}

func TestPlayerCardIsCompactOnANarrowTerminal(t *testing.T) {
	wide, _ := renderCard(card("playing", 0, time.Minute), 100)
	narrow, _ := renderCard(card("playing", 0, time.Minute), 50)
	if !strings.Contains(ansi.Strip(wide), "speed") || strings.Contains(ansi.Strip(narrow), "speed") {
		t.Fatal("the words should drop below 62 cells")
	}
	for _, line := range strings.Split(ansi.Strip(narrow), "\n") {
		if ansi.StringWidth(line) > 50 {
			t.Errorf("line wider than the card: %q", line)
		}
	}
}

func TestPlayerCardFailedIsDanger(t *testing.T) {
	c := card("failed", 0, 0)
	c.Status = "failed: service gone"
	out, _ := renderCard(c, 100)
	if !strings.Contains(ansi.Strip(out), "failed: service gone") {
		t.Fatal("the error is not on the card")
	}
}

func TestPlayerCardWithNoStateStillRenders(t *testing.T) {
	out, lm := playerPanel(Message{Role: PlayerRole}, 80)
	if out == "" || len(lm) != len(strings.Split(out, "\n")) {
		t.Fatal("a card with no player state did not render")
	}
}

func TestPlayerCardCopiesNothing(t *testing.T) {
	_, lm := renderCard(card("playing", 0, time.Minute), 100)
	for i, sl := range lm {
		if len(sl.Hits) > 0 && sl.Text != "" {
			t.Errorf("row %d offers %q to a copy: the controls are not text", i, sl.Text)
		}
	}
}

func TestPlayerCardKeyChangesOnlyWhenTheDrawingDoes(t *testing.T) {
	a := card("playing", 10*time.Second, time.Minute)
	b := card("playing", 10*time.Second+200*time.Millisecond, time.Minute)
	if a.Key() != b.Key() {
		t.Fatal("a sub-second move re-rendered the card")
	}
	for name, mut := range map[string]func(c *PlayerCard){
		"state":  func(c *PlayerCard) { c.State = "paused" },
		"second": func(c *PlayerCard) { c.Pos += time.Second },
		"speed":  func(c *PlayerCard) { c.Speed = 2 },
		"levels": func(c *PlayerCard) { c.Levels = []float64{1, 1, 1} },
		"status": func(c *PlayerCard) { c.Status = "x" },
		"frame":  func(c *PlayerCard) { c.Frame++ },
		"final":  func(c *PlayerCard) { c.Final = false },
	} {
		c := *a
		mut(&c)
		if c.Key() == a.Key() {
			t.Errorf("a %s change did not change the render key", name)
		}
	}
	if (*PlayerCard)(nil).Key() != "" {
		t.Fatal("nil key")
	}
}

func TestMessageListDrawsAndRedrawsThePlayerCard(t *testing.T) {
	ml := MessageList{Width: 100, Messages: []Message{{Role: PlayerRole, Ephemeral: true, Player: card("playing", 0, time.Minute)}}}
	first, lm := ml.Render()
	if !strings.Contains(ansi.Strip(first), "read aloud") || len(lm) == 0 || lm[len(lm)-1].Owner != 0 {
		t.Fatalf("first render:\n%s", ansi.Strip(first))
	}
	ml.Messages[0].Player = card("paused", 5*time.Second, time.Minute)
	second, _ := ml.Render()
	if first == second || !strings.Contains(ansi.Strip(second), "paused") {
		t.Fatal("a changed card was served from the render cache")
	}
}

func TestClockAndSpeedLabel(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "0:00", 59 * time.Second: "0:59", 61 * time.Second: "1:01", 3599 * time.Second: "59:59", 3661 * time.Second: "1:01:01", -5 * time.Second: "0:00"} {
		if got := Clock(d); got != want {
			t.Errorf("Clock(%v) = %q, want %q", d, got, want)
		}
	}
	for f, want := range map[float64]string{1: "1×", 0.75: "0.75×", 1.25: "1.25×", 1.5: "1.5×", 2: "2×"} {
		if got := SpeedLabel(f); got != want {
			t.Errorf("SpeedLabel(%v) = %q, want %q", f, got, want)
		}
	}
}
