package agentprofile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/svgguard"
)

// The crews and the agents a person addresses by name each have a persona: a
// display name, four colours, a personality and a drawn avatar.
var personaProfiles = []string{
	"belai:vuln-scout", "belai:patcher", "belai:verifier",
	"belai:scout", "belai:builder", "belai:reviewer",
	"belai:triage-vulns", "belai:debug", "belai:fanout", "belai:plan-handoff",
}

func TestBuiltinPersonas(t *testing.T) {
	resetDir(t)
	seen := map[string]string{}
	for _, name := range personaProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		if p.DisplayName == "" || len(p.Palette) != PaletteSize || p.Personality.IsZero() || p.AvatarID == "" {
			t.Errorf("%s: needs a display name, a palette, a personality and an avatar", name)
		}
		if p.Personality != nil && (p.Personality.ReportStyle == "" || len(p.Personality.Focus) == 0 || len(p.Personality.Vocabulary) == 0) {
			t.Errorf("%s: the personality should fill all three parts", name)
		}
		if p.AvatarID != BuiltinID("avatar:"+name) {
			t.Errorf("%s: avatar_id is not the built-in avatar id", name)
		}
		key := DisplayNameKey(p.DisplayName)
		if other, dup := seen[key]; dup {
			t.Errorf("%s and %s share the display name %q", name, other, p.DisplayName)
		}
		seen[key] = name
		if strings.HasPrefix(p.DisplayName, "belai") {
			t.Errorf("%s: a display name should not look like a built-in name", name)
		}
	}
}

func TestSecurityCrewPersonas(t *testing.T) {
	resetDir(t)
	for name, want := range map[string]string{
		"belai:vuln-scout": "Rubber Duck",
		"belai:patcher":    "Kremvax",
		"belai:verifier":   "Dark Avenger",
	} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if p.DisplayName != want {
			t.Errorf("%s is %q, want %q", name, p.DisplayName, want)
		}
	}
}

func TestPersonaIsStyleOnly(t *testing.T) {
	resetDir(t)
	for _, name := range personaProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		persona := p.Persona()
		if !strings.Contains(persona, "never change what you are allowed to do") {
			t.Errorf("%s: the personality must ride under the style framing", name)
		}
		if !bytes.Equal([]byte(p.Behavioural().Persona()), []byte(persona)) {
			t.Errorf("%s: the cosmetic fields must not change the persona text", name)
		}
	}
}

// Every avatar is a drawing svgguard admits unchanged, the same gate a drawing
// the builder asks a host for passes, and none is shared.
func TestPersonaAvatarsAreAdmitted(t *testing.T) {
	resetDir(t)
	used := map[string]bool{}
	for _, name := range personaProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join("builtin", "avatars", p.AvatarID+".svg"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := svgguard.Sanitize(b)
		if err != nil {
			t.Fatalf("%s: the guard refuses its avatar: %v", name, err)
		}
		if !bytes.Equal(out, b) {
			t.Errorf("%s: the avatar is not the guard's own serialisation", name)
		}
		if used[string(b)] {
			t.Errorf("%s: two profiles share an avatar", name)
		}
		used[string(b)] = true
	}
	files, _ := filepath.Glob(filepath.Join("builtin", "avatars", "*.svg"))
	if len(files) != len(personaProfiles) {
		t.Errorf("%d avatar files for %d profiles: remove the ones no profile names", len(files), len(personaProfiles))
	}
}

// The website draws a built-in's avatar from its own copy of the file, so the
// two must be the same bytes. The check runs where the website is checked out
// beside this repository, as the dependency table's does for the CLI.
func TestPersonaAvatarsMatchTheWebsite(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "website", "public", "belai-avatars")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("the website is not checked out beside this repository")
	}
	for _, name := range personaProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		mine, _ := os.ReadFile(filepath.Join("builtin", "avatars", p.AvatarID+".svg"))
		theirs, err := os.ReadFile(filepath.Join(dir, p.AvatarID+".svg"))
		if err != nil {
			t.Errorf("%s: the website has no copy of the avatar: %v", name, err)
			continue
		}
		if !bytes.Equal(mine, theirs) {
			t.Errorf("%s: the website's avatar differs from this one", name)
		}
	}
}
