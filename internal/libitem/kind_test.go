package libitem

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

func TestKindTable(t *testing.T) {
	want := []struct {
		kind     Kind
		segment  string
		ext      string
		max      int
		markdown bool
	}{
		{Skill, "skills", "md", 32 << 10, true},
		{Prompt, "prompts", "md", 32 << 10, true},
		{Process, "processes", "json", 16 << 10, false},
		{Repo, "repos", "json", 16 << 10, false},
		{Budget, "budgets", "json", 16 << 10, false},
		{Provider, "providers", "json", 32 << 10, false},
		{Rewrite, "rewrites", "json", 16 << 10, false},
	}
	if len(Kinds()) != len(want) {
		t.Fatalf("%d kinds, want %d", len(Kinds()), len(want))
	}
	for i, w := range want {
		k := Kinds()[i]
		if k != w.kind || k.Segment() != w.segment || k.Info().Ext() != w.ext || k.MaxBytes() != w.max || k.IsMarkdown() != w.markdown || !k.Valid() {
			t.Errorf("kind %d = %+v", i, k.Info())
		}
		if got, ok := Parse(string(w.kind)); !ok || got != w.kind {
			t.Errorf("Parse(%q) = %q, %v", w.kind, got, ok)
		}
		if got, ok := ParseSegment(w.segment); !ok || got != w.kind {
			t.Errorf("ParseSegment(%q) = %q, %v", w.segment, got, ok)
		}
		// The two spellings are not interchangeable.
		if _, ok := Parse(w.segment); ok {
			t.Errorf("Parse accepted the segment %q", w.segment)
		}
		if _, ok := ParseSegment(string(w.kind)); ok {
			t.Errorf("ParseSegment accepted the kind %q", w.kind)
		}
	}
	if Kind("nope").Valid() || Kind("nope").Segment() != "" || Kind("nope").MaxBytes() != 0 {
		t.Error("an unknown kind has properties")
	}
	if !Budget.Singleton() || !Provider.Singleton() || !Rewrite.Singleton() || Skill.Singleton() || Repo.Singleton() {
		t.Error("Singleton is wrong")
	}
}

func TestValidName(t *testing.T) {
	ok := []string{"a", "0", "release", "my.skill", "my_skill", "a-b", "a1.b_c-d", strings.Repeat("a", 64)}
	bad := []string{"", "-a", ".a", "_a", "A", "aB", "a b", "a/b", "a\n", "café", strings.Repeat("a", 65), "a:b"}
	for _, k := range []Kind{Skill, Prompt, Process, Repo, Budget, Provider} {
		for _, n := range ok {
			if !ValidName(k, n) {
				t.Errorf("ValidName(%s, %q) = false", k, n)
			}
		}
		for _, n := range bad {
			if ValidName(k, n) {
				t.Errorf("ValidName(%s, %q) = true", k, n)
			}
		}
	}
	if !ValidName(Rewrite, "bash_rewrite") || ValidName(Rewrite, "rewrite") || ValidName(Rewrite, "Bash_rewrite") || ValidName(Rewrite, "") {
		t.Error("a rewrite is named bash_rewrite and nothing else")
	}
}

func TestValidateRefusesWhatItCannotCheck(t *testing.T) {
	if _, err := Validate("nope", []byte("x")); err == nil {
		t.Error("an unknown kind validated")
	}
	if _, err := Validate(Skill, []byte(strings.Repeat("x", 64<<10+1))); err == nil || !strings.Contains(err.Error(), "the most is 32768") {
		t.Errorf("an oversized raw document: %v", err)
	}
}

func TestErrorTextIsClipped(t *testing.T) {
	_, err := Validate(Prompt, []byte("---\nname: "+strings.Repeat("A", 500)+"\n---\n\nx"))
	if err == nil || len(err.Error()) > 300 {
		t.Fatalf("err = %v", err)
	}
}

// The page that states the limits states the ones the code enforces.
func TestLibraryItemsPageStatesTheLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/library-items.md")), " ")
	for _, want := range []string{
		"at most 200 live items of one kind",
		"A name is 1 to 64 characters",
		"A skill or a prompt document is at most 32 KiB",
		"(required) is at most 300 bytes",
		"allowed-tools` holds at most 64",
		"`license` is at most 128 bytes",
		"`compatibility` at most 500",
		"`metadata` is a one-line string of at most 1024 bytes",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/library-items.md does not say %q", want)
		}
	}
	if MaxItemsPerKind != 200 || MaxNameBytes != 64 || MaxDescriptionBytes != 300 || MaxSkillLicense != 128 || MaxSkillCompatibility != 500 || MaxSkillMetadata != 1024 || MaxSkillAllowedTools != 64 {
		t.Error("a limit changed; update docs/library-items.md and this test")
	}
}
