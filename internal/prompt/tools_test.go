package prompt

import (
	"strings"
	"testing"
)

func TestSummariseTakesTheFirstSentence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"first sentence", "Read a file. Then do more. And more.", "Read a file."},
		{"single sentence", "Print the current date and time (UTC).", "Print the current date and time (UTC)."},
		{"no terminator", "Search the web", "Search the web"},
		{"empty", "   ", ""},
		// A period that is not a sentence end must not truncate the summary:
		// "e.g." and "1 MiB." are both followed by a space in real
		// descriptions, so only a ". " that ends a clause is usable — this
		// pins the conservative behaviour rather than pretending otherwise.
		{"abbreviation splits at the first period-space", "Run a command, e.g. git status. More.", "Run a command, e.g."},
		{"trims surrounding space", "  Do a thing.  Another.  ", "Do a thing."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Summarise(tc.in); got != tc.want {
				t.Fatalf("Summarise(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// No tools means no block: the classifier turn carries no tools and must not
// carry an empty briefing that implies it has some.
// The global rules carry the path-resolution convention centrally: absolute
// under a root, relative to the working directory, or session-root-relative
// when a leading "/" lands in no root.
func TestToolsBlockStatesPathResolutionRule(t *testing.T) {
	got := ToolsBlock(ToolsOptions{Workdir: "/repo", Tools: []ToolDoc{{Name: "Read"}}})
	for _, want := range []string{
		"absolute filesystem path under one of the roots above",
		"or relative to the working directory",
		"A path starting with `/` that is not under any root is read as relative to the session root",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
}

func TestToolsBlockEmptyWithoutTools(t *testing.T) {
	if got := ToolsBlock(ToolsOptions{Workdir: "/repo", PlanMode: true}); got != "" {
		t.Fatalf("ToolsBlock with no tools = %q, want empty", got)
	}
}

func TestToolsBlockListsToolsSortedWithSummaries(t *testing.T) {
	got := ToolsBlock(ToolsOptions{
		Workdir: "/repo",
		Tools: []ToolDoc{
			{Name: "Read", Summary: "Read a file."},
			{Name: "Glob", Summary: "Find files by name."},
			{Name: "Pwd"},
		},
	})
	for _, want := range []string{
		"- Glob — Find files by name.\n",
		"- Read — Read a file.\n",
		"- Pwd\n",
		"Working directory: /repo.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "- Glob") > strings.Index(got, "- Read") {
		t.Errorf("tools are not sorted by name:\n%s", got)
	}
	if strings.Contains(got, "Mode: plan") {
		t.Errorf("agent-mode block must not carry plan-mode restrictions:\n%s", got)
	}
}

// A tool with no summary still has to appear: an entry missing from the index
// reads as a tool that is not available.
func TestToolsBlockKeepsToolsWithoutSummaries(t *testing.T) {
	got := ToolsBlock(ToolsOptions{Tools: []ToolDoc{{Name: "Env"}}})
	if !strings.Contains(got, "- Env\n") {
		t.Fatalf("block missing the summary-less tool:\n%s", got)
	}
	if strings.Contains(got, "Env — ") {
		t.Fatalf("block rendered an empty summary dash:\n%s", got)
	}
}

// Plan mode must say what is gone, not only what remains: a model that is
// never told Bash is unavailable keeps calling it and keeps being refused.
func TestToolsBlockPlanModeNamesWhatIsUnavailable(t *testing.T) {
	got := ToolsBlock(ToolsOptions{
		PlanMode: true,
		Tools:    []ToolDoc{{Name: "Read", Summary: "Read a file."}},
	})
	for _, want := range []string{"Mode: plan", "Bash is not available", "Write, Edit"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan block missing %q:\n%s", want, got)
		}
	}
	// The approval rule is about mutating calls, which plan mode has none of.
	if strings.Contains(got, "ask for approval") {
		t.Errorf("plan block must not mention the approval gate:\n%s", got)
	}
}

// The cross-cutting rules are the part the per-tool schema cannot carry, so
// they must be in every non-empty block.
func TestToolsBlockAlwaysCarriesTheSharedRules(t *testing.T) {
	for _, planMode := range []bool{false, true} {
		got := ToolsBlock(ToolsOptions{PlanMode: planMode, Tools: []ToolDoc{{Name: "Read"}}})
		for _, want := range []string{
			"confined to the working directory",
			"untrusted content",
			"truncated",
			"call a tool only if it appears here",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("planMode=%v block missing %q:\n%s", planMode, want, got)
			}
		}
	}
}

// An unknown working directory is omitted rather than guessed at, matching
// how the identity section treats an unknown provider or model.
func TestToolsBlockOmitsUnknownWorkdir(t *testing.T) {
	got := ToolsBlock(ToolsOptions{Tools: []ToolDoc{{Name: "Read"}}})
	if strings.Contains(got, "Working directory:") {
		t.Fatalf("block invented a working directory:\n%s", got)
	}
}

// Added workspace directories are named in the briefing and included in the
// confinement rule.
func TestToolsBlockNamesExtraRoots(t *testing.T) {
	got := ToolsBlock(ToolsOptions{
		Workdir:    "/repo",
		ExtraRoots: []string{"/other"},
		Tools:      []ToolDoc{{Name: "Read", Summary: "Read a file."}},
	})
	for _, want := range []string{
		"Working directory: /repo.",
		"Additional workspace roots: /other",
		"confined to the working directory and the additional workspace roots listed above",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
}
