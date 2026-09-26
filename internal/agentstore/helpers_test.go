package agentstore

import (
	"testing"
	"time"
)

func TestAdapterForDispatch(t *testing.T) {
	home := t.TempDir()
	r := newTestRegistry(home, home)

	cases := []struct {
		format Format
		want   bool // non-nil adapter expected
	}{
		{FormatJSONLClaude, true},
		{FormatJSONLCodex, true},
		{FormatJSONLPi, true},
		{FormatJSONLBelai, true},
		{FormatJSONLPrompts, true},
		{FormatSQLiteGoose, true},
		{FormatSQLiteOpenCode, true},
		{FormatJSONVSCode, true},
		{FormatJSONGeneric, true},
		{FormatMarkdown, false}, // memory handled separately
		{"bogus", false},
		{"", false},
	}
	for _, c := range cases {
		got := r.adapterFor(Agent{Format: c.format})
		if (got != nil) != c.want {
			t.Errorf("adapterFor(%q) = %v, want non-nil=%v", c.format, got, c.want)
		}
	}
}

func TestDefaultProject(t *testing.T) {
	r := &Registry{workdir: "/home/u/proj/belai"}
	cases := []struct {
		q    SessionQuery
		want string
	}{
		{SessionQuery{}, "/home/u/proj/belai"},
		{SessionQuery{Project: "other"}, "other"},
		{SessionQuery{AllProjects: true}, ""},
		{SessionQuery{AllProjects: true, Project: "other"}, ""}, // all-projects wins
	}
	for _, c := range cases {
		if got := r.defaultProject(c.q); got != c.want {
			t.Errorf("defaultProject(%+v) = %q, want %q", c.q, got, c.want)
		}
	}
}

func TestSourceMatchesProject(t *testing.T) {
	cases := []struct {
		src     Source
		project string
		want    bool
	}{
		{Source{Project: "/home/u/proj/belai"}, "", true},
		{Source{Project: "/home/u/proj/belai"}, "belai", true},
		{Source{Project: "/home/u/proj/belai"}, "other", false},
		{Source{Project: ""}, "belai", false}, // unknown project never matches
	}
	for _, c := range cases {
		if got := sourceMatchesProject(c.src, c.project); got != c.want {
			t.Errorf("sourceMatchesProject(%+v, %q) = %v, want %v", c.src, c.project, got, c.want)
		}
	}
}

func TestSourceMatchesTime(t *testing.T) {
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	src := Source{ModTime: base}
	cases := []struct {
		name  string
		since time.Time
		until time.Time
		want  bool
	}{
		{"no bounds", time.Time{}, time.Time{}, true},
		{"after since", base.Add(-time.Hour), time.Time{}, true},
		{"before since", base.Add(time.Hour), time.Time{}, false},
		{"before until", time.Time{}, base.Add(time.Hour), true},
		{"after until", time.Time{}, base.Add(-time.Hour), false},
		{"inside", base.Add(-time.Hour), base.Add(time.Hour), true},
	}
	for _, c := range cases {
		if got := sourceMatchesTime(src, c.since, c.until); got != c.want {
			t.Errorf("%s: sourceMatchesTime = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHitMatchesProject(t *testing.T) {
	cases := []struct {
		hit     Hit
		project string
		want    bool
	}{
		{Hit{Project: "/home/u/proj/belai"}, "", true},
		{Hit{Project: "/home/u/proj/belai"}, "belai", true},
		{Hit{Project: "/home/u/proj/belai"}, "other", false},
		{Hit{Project: ""}, "belai", false},
	}
	for _, c := range cases {
		if got := hitMatchesProject(c.hit, c.project); got != c.want {
			t.Errorf("hitMatchesProject(%+v, %q) = %v, want %v", c.hit, c.project, got, c.want)
		}
	}
}

func TestRoleMatches(t *testing.T) {
	cases := []struct {
		want, got string
		match     bool
	}{
		{"", "user", true}, // no filter
		{"user", "user", true},
		{"user", "USER", true}, // case-insensitive
		{"user", "assistant", false},
	}
	for _, c := range cases {
		if got := roleMatches(c.want, c.got); got != c.match {
			t.Errorf("roleMatches(%q, %q) = %v, want %v", c.want, c.got, got, c.match)
		}
	}
}

func TestAgentFormatAndIsSQLite(t *testing.T) {
	if got := agentFormat("claude-code"); got != FormatJSONLClaude {
		t.Errorf("agentFormat(claude-code) = %q", got)
	}
	if got := agentFormat("nope"); got != "" {
		t.Errorf("agentFormat(nope) = %q, want empty", got)
	}
	if !isSQLiteFormat(FormatSQLiteGoose) || !isSQLiteFormat(FormatSQLiteOpenCode) {
		t.Error("SQLite formats must be recognised")
	}
	if isSQLiteFormat(FormatJSONLClaude) || isSQLiteFormat("") {
		t.Error("non-SQLite formats must not be recognised")
	}
}
