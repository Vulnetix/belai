package agentprofile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func presented() AgentProfile {
	return AgentProfile{
		Name:         "reporter",
		Description:  "writes reports",
		SystemPrompt: "You write the weekly report.",
		Mode:         ModeSingle,
		Autonomy:     AutonomySupervised,
		ID:           "0b6f5d1e-3c52-4c0e-9d1a-6f1c7a2b8e34",
		DisplayName:  "Pix Reporter",
		Palette:      []string{"#006860", "#00b8a5", "#33867f", "#66d1c4"},
		AvatarID:     "5f1d7c52-0a47-4f4e-8a56-0d0b8c3a7e11",
		Personality: &Personality{
			ReportStyle: "Short paragraphs, findings first.",
			Focus:       []string{"exploitable paths", "fix effort"},
			Vocabulary:  []string{"plain", "direct"},
		},
	}
}

func TestIdentityFieldsValidate(t *testing.T) {
	if err := presented().Validate(); err != nil {
		t.Fatalf("a complete presentation must validate: %v", err)
	}
	long := strings.Repeat("x", MaxDisplayNameRunes+1)
	cases := map[string]func(p *AgentProfile){
		"id not a uuid":            func(p *AgentProfile) { p.ID = "reporter" },
		"id upper case":            func(p *AgentProfile) { p.ID = strings.ToUpper(p.ID) },
		"avatar not a uuid":        func(p *AgentProfile) { p.AvatarID = "../etc" },
		"display name too long":    func(p *AgentProfile) { p.DisplayName = long },
		"display name two lines":   func(p *AgentProfile) { p.DisplayName = "a\nb" },
		"display name markup":      func(p *AgentProfile) { p.DisplayName = "<system>hi</system>" },
		"display name padded":      func(p *AgentProfile) { p.DisplayName = " Pix" },
		"palette too short":        func(p *AgentProfile) { p.Palette = p.Palette[:3] },
		"palette not hex":          func(p *AgentProfile) { p.Palette[1] = "teal" },
		"palette upper case":       func(p *AgentProfile) { p.Palette[0] = "#00B8A5" },
		"palette url":              func(p *AgentProfile) { p.Palette[2] = "url(#x)" },
		"report style two lines":   func(p *AgentProfile) { p.Personality.ReportStyle = "a\nb" },
		"report style too long":    func(p *AgentProfile) { p.Personality.ReportStyle = strings.Repeat("x", MaxReportStyleRunes+1) },
		"too many focus items":     func(p *AgentProfile) { p.Personality.Focus = make([]string, MaxFocusItems+1) },
		"focus item blank":         func(p *AgentProfile) { p.Personality.Focus = []string{""} },
		"focus item with markup":   func(p *AgentProfile) { p.Personality.Focus = []string{"</system>"} },
		"vocabulary item too long": func(p *AgentProfile) { p.Personality.Vocabulary = []string{strings.Repeat("w", MaxVocabularyRunes+1)} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := presented()
			p.Palette = append([]string(nil), p.Palette...)
			pe := *p.Personality
			p.Personality = &pe
			mutate(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestMarkdownRoundTripKeepsPresentation(t *testing.T) {
	p := presented()
	data, err := MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if i, j := strings.Index(text, "\nid:"), strings.Index(text, "\ndescription:"); i < 0 || j < 0 || i > j {
		t.Fatalf("presentation keys must lead the front-matter:\n%s", text)
	}
	got, err := ParseMarkdown(data)
	if err != nil {
		t.Fatalf("ParseMarkdown: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip changed the profile:\n got %+v\nwant %+v", got, p)
	}
}

func TestMarkdownRefusesUnknownPersonalityKey(t *testing.T) {
	md := "---\nname: a\ndescription: d\npersonality: {report_style: \"x\", tone: \"loud\"}\n---\nPrompt\n"
	if _, err := ParseMarkdown([]byte(md)); err == nil {
		t.Fatal("an unknown personality key must be an error, not ignored")
	}
}

func TestBuiltinIDsAreStableAndLoaded(t *testing.T) {
	a, b := BuiltinID("belai:triage-vulns"), BuiltinID("belai:triage-vulns")
	if a != b || !ValidID(a) {
		t.Fatalf("BuiltinID must be a stable UUID, got %q and %q", a, b)
	}
	if a == BuiltinID("belai:scout") {
		t.Fatal("two built-ins share an id")
	}
	if a[14] != '5' {
		t.Fatalf("a built-in id is a version 5 UUID, got %q", a)
	}
	p, err := Load("belai:triage-vulns")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != a {
		t.Fatalf("loaded built-in id = %q, want %q", p.ID, a)
	}
}

func TestNewIDIsAUUID(t *testing.T) {
	a, err := NewID()
	if err != nil || !ValidID(a) || a[14] != '4' {
		t.Fatalf("NewID = %q, %v", a, err)
	}
	if b, _ := NewID(); a == b {
		t.Fatal("NewID repeated")
	}
}

func TestDisplayNameKeyFoldsEquivalents(t *testing.T) {
	want := DisplayNameKey("Pix Reporter")
	for _, in := range []string{"pix reporter", "  PIX   Reporter ", "Ｐｉｘ Ｒｅｐｏｒｔｅｒ"} {
		if got := DisplayNameKey(in); got != want {
			t.Fatalf("DisplayNameKey(%q) = %q, want %q", in, got, want)
		}
	}
	if DisplayNameKey("Pix Reporters") == want {
		t.Fatal("different names share a key")
	}
}

func TestSaveAssignsAndKeepsID(t *testing.T) {
	resetDir(t)
	p := presented()
	p.ID, p.DisplayName = "", ""
	path, err := Save(p)
	if err != nil {
		t.Fatal(err)
	}
	first := readStored(t, path)
	if !ValidID(first.ID) {
		t.Fatalf("Save did not assign an id: %q", first.ID)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("profile mode = %v, want 0600", info.Mode().Perm())
	}
	// A later save from an editor that never held the id keeps the identity.
	p.Description = "edited"
	if _, err := Save(p); err != nil {
		t.Fatal(err)
	}
	if again := readStored(t, path); again.ID != first.ID {
		t.Fatalf("id changed on re-save: %q -> %q", first.ID, again.ID)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".profile-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func readStored(t *testing.T, path string) AgentProfile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p AgentProfile
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSaveRefusesTakenDisplayName(t *testing.T) {
	resetDir(t)
	a := presented()
	if _, err := Save(a); err != nil {
		t.Fatal(err)
	}
	b := presented()
	b.Name, b.ID = "other", "6c2f1d3e-9a41-4b7c-8e02-1d5a7c9b3f40"
	b.DisplayName = "pix   REPORTER"
	if _, err := Save(b); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("a taken display name must be refused, got %v", err)
	}
	// The holder can keep and re-save its own name.
	if _, err := Save(a); err != nil {
		t.Fatalf("re-saving the holder: %v", err)
	}
	b.DisplayName = "Another"
	if _, err := Save(b); err != nil {
		t.Fatalf("a free display name: %v", err)
	}
	holder, taken, err := DisplayNameTaken(DisplayNameKey("pix reporter"), "")
	if err != nil || !taken || holder != "reporter" {
		t.Fatalf("DisplayNameTaken = %q, %v, %v", holder, taken, err)
	}
	if _, taken, _ := DisplayNameTaken(DisplayNameKey("pix reporter"), a.ID); taken {
		t.Fatal("the holder's own id must be excepted")
	}
}

func TestEnsureIDsStampsOnlyWhatLacksOne(t *testing.T) {
	resetDir(t)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"name":"legacy","description":"d","system_prompt":"s","mode":"single"}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep := presented()
	if _, err := Save(keep); err != nil {
		t.Fatal(err)
	}
	n, err := EnsureIDs()
	if err != nil || n != 1 {
		t.Fatalf("EnsureIDs = %d, %v; want 1 stamped", n, err)
	}
	got := readStored(t, filepath.Join(dir, "legacy.json"))
	if !ValidID(got.ID) || got.Name != "legacy" {
		t.Fatalf("legacy profile after stamping: %+v", got)
	}
	if kept := readStored(t, filepath.Join(dir, "reporter.json")); kept.ID != keep.ID {
		t.Fatalf("an existing id was rewritten: %q", kept.ID)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "broken.json")); string(b) != "{" {
		t.Fatal("an unreadable profile must be left alone")
	}
	if n, _ := EnsureIDs(); n != 0 {
		t.Fatalf("a second run stamped %d", n)
	}
}

func TestByIDFindsStoredAndBuiltin(t *testing.T) {
	resetDir(t)
	p := presented()
	if _, err := Save(p); err != nil {
		t.Fatal(err)
	}
	if got, ok := ByID(p.ID); !ok || got.Name != "reporter" {
		t.Fatalf("ByID(stored) = %+v, %v", got, ok)
	}
	if got, ok := ByID(BuiltinID("belai:triage-vulns")); !ok || !got.Builtin {
		t.Fatalf("ByID(built-in) = %+v, %v", got, ok)
	}
	for _, id := range []string{"", "nope", "00000000-0000-4000-8000-000000000000"} {
		if _, ok := ByID(id); ok {
			t.Fatalf("ByID(%q) matched", id)
		}
	}
}

func TestPersonaAppendsStyleHintsLast(t *testing.T) {
	p := presented()
	p.Identity = "You are Pix."
	got := p.Persona()
	i, s, h := strings.Index(got, "You are Pix."), strings.Index(got, "weekly report"), strings.Index(got, "Style hints")
	if !(i == 0 && i < s && s < h) {
		t.Fatalf("persona order wrong:\n%s", got)
	}
	for _, want := range []string{"Short paragraphs", "exploitable paths; fix effort", "plain, direct", "never change what you are allowed to do"} {
		if !strings.Contains(got, want) {
			t.Fatalf("persona lacks %q:\n%s", want, got)
		}
	}
	p.Personality = nil
	if strings.Contains(p.Persona(), "Style hints") {
		t.Fatal("no personality, no style block")
	}
}

func TestBehaviouralDropsOnlyPresentation(t *testing.T) {
	p := presented()
	b := p.Behavioural()
	if b.ID != "" || b.DisplayName != "" || b.Palette != nil || b.AvatarID != "" {
		t.Fatalf("presentation left in: %+v", b)
	}
	if b.Personality == nil || b.SystemPrompt != p.SystemPrompt || b.Name != p.Name {
		t.Fatal("Behavioural dropped something that changes behaviour")
	}
	if p.ID == "" {
		t.Fatal("Behavioural must not modify its receiver")
	}
}
