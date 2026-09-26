package components

import (
	"strings"
	"testing"

	"github.com/muesli/termenv"

	"github.com/vulnetix/belai/internal/rolemanager"
)

func rmMessage(level rolemanager.Level, summary, outcome string, tone rolemanager.Tone) Message {
	return Message{
		Role:  "rolemanager",
		Level: level,
		RM: rolemanager.Description{
			Summary: summary,
			Outcome: outcome,
			Tone:    tone,
			Levels:  level,
		},
	}
}

// TestRolemanagerMessageRendersInBelaiPanel pins the render-only feed: the
// activity renders as a belai panel line, the LineMap matches the rendered
// line count, and under TrueColor the outcome word carries colour.
func TestRolemanagerMessageRendersInBelaiPanel(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		ml := MessageList{
			Width:        80,
			ShowTools:    true,
			ShowEdits:    true,
			InternalWork: rolemanager.LevelAll,
			Messages: []Message{
				rmMessage(rolemanager.LevelSecurity,
					"Checked what the shell command returned for hidden instructions",
					"clean", rolemanager.ToneClear),
			},
		}
		rendered, lm := ml.Render()
		lines := strings.Split(rendered, "\n")
		if len(lm) != len(lines) {
			t.Fatalf("map %d entries vs %d rendered lines", len(lm), len(lines))
		}
		if !strings.Contains(rendered, "Checked what the shell command returned") {
			t.Fatalf("missing summary: %q", rendered)
		}
		if !strings.Contains(rendered, "clean") {
			t.Fatalf("missing outcome: %q", rendered)
		}
		if !strings.Contains(rendered, "\x1b[") {
			t.Fatalf("truecolor render should carry colour: %q", rendered)
		}
		assertLineMapInvariant(t, "rolemanager", rendered, lm, max(80, messageMinWidth))
	})
}

// TestRolemanagerLevelFiltering pins the additive levels: each level keeps the
// rows at or below it and drops the rest.
func TestRolemanagerLevelFiltering(t *testing.T) {
	messages := []Message{
		rmMessage(rolemanager.LevelDecisions, "DECISION", "done", rolemanager.ToneClear),
		rmMessage(rolemanager.LevelSecurity, "SECURITY", "clean", rolemanager.ToneClear),
		rmMessage(rolemanager.LevelAll, "BOOKKEEPING", "done", rolemanager.ToneNeutral),
	}

	cases := []struct {
		level  rolemanager.Level
		want   []string
		absent []string
	}{
		{rolemanager.LevelHidden, nil, []string{"DECISION", "SECURITY", "BOOKKEEPING"}},
		{rolemanager.LevelDecisions, []string{"DECISION"}, []string{"SECURITY", "BOOKKEEPING"}},
		{rolemanager.LevelSecurity, []string{"DECISION", "SECURITY"}, []string{"BOOKKEEPING"}},
		{rolemanager.LevelAll, []string{"DECISION", "SECURITY", "BOOKKEEPING"}, nil},
	}

	for _, tc := range cases {
		ml := MessageList{
			Messages:     messages,
			Width:        80,
			ShowTools:    true,
			ShowEdits:    true,
			InternalWork: tc.level,
		}
		rendered, _ := ml.Render()
		for _, w := range tc.want {
			if !strings.Contains(rendered, w) {
				t.Fatalf("level %d: want %q in:\n%s", tc.level, w, rendered)
			}
		}
		for _, a := range tc.absent {
			if strings.Contains(rendered, a) {
				t.Fatalf("level %d: %q should be filtered out:\n%s", tc.level, a, rendered)
			}
		}
	}
}
