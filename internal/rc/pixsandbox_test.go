package rc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/vulnetixcreds"
)

// These tests pin what an unmodified Belai does with the inputs a hosted Pix
// Sandbox gives it (docs/pix-sandbox.md). The machine's launcher lives outside
// this repository; if one of these changes, that launcher has to change with it.

const pixHostID = "7c1f2a3e-4b5d-4e6f-8a9b-0c1d2e3f4a5b"

// pixSettings is the settings file the launcher writes, as JSON.
const pixSettings = `{
  "sync": {"enabled": true, "remote_prompts": true},
  "firewall": {"enabled": true, "active": "vulnetix"},
  "classifier": {"kind": "jev", "provider": "typesafe", "model": "jev-latest"}
}`

func pixHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "sync"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sync", "host-id"), []byte(pixHostID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(pixSettings), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// A uuid seeded into sync/host-id is the host id and survives every later read.
func TestPixSeededHostIDIsKept(t *testing.T) {
	home := pixHome(t)
	for i := 0; i < 2; i++ {
		got, err := sessionsync.HostID(home)
		if err != nil || got != pixHostID {
			t.Fatalf("read %d: host id = %q, %v", i, got, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(home, "sync", "host-id"))
	if strings.TrimSpace(string(data)) != pixHostID {
		t.Fatalf("host-id file was rewritten: %q", data)
	}
}

// A file that is not a uuid is replaced by a fresh id, as on any install.
func TestPixMalformedHostIDIsReplaced(t *testing.T) {
	home := pixHome(t)
	if err := os.WriteFile(filepath.Join(home, "sync", "host-id"), []byte("pix-not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := sessionsync.HostID(home)
	if err != nil || got == "pix-not-a-uuid" || got == pixHostID {
		t.Fatalf("host id = %q, %v", got, err)
	}
}

// The launcher's settings file is ordinary global settings: sync and remote
// prompts on, the Vulnetix AI Firewall active, guardrails on by default.
func TestPixSettingsLoadAsDocumented(t *testing.T) {
	pixHome(t)
	s, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if !s.SyncEnabled() || !s.SyncRemotePromptsEnabled() {
		t.Error("sync or remote prompts are off")
	}
	if !s.FirewallEnabled() || s.FirewallActive() != "vulnetix" {
		t.Errorf("firewall enabled=%v active=%q", s.FirewallEnabled(), s.FirewallActive())
	}
	if !s.GuardrailsEnabled() {
		t.Error("guardrails are off without being asked")
	}
	// The sandbox writes the kind's old name, jev; it loads as systemone.
	if c := s.Classifier; c == nil || c.Kind != config.SystemOneKind || c.Provider != decisions.TypeSafeProvider || c.Model != decisions.TypeSafeDefaultModel {
		t.Errorf("classifier = %+v", c)
	}
	if err := config.ValidateFirewall(s); err != nil {
		t.Errorf("firewall settings invalid: %v", err)
	}
}

// Jev resolves to the fixed TypeSafe origin with the key read from
// TYPESAFE_API_KEY, and a machine with no key never sends an anonymous call.
func TestPixClassifierIsJevOnTypeSafe(t *testing.T) {
	pixHome(t)
	s, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	main := run.Config{Provider: "openrouter", Model: "openai/gpt-5"}
	cc, err := run.ResolveClassifier(main, s.Classifier, run.EnvSource(func(k string) string {
		if k == decisions.TypeSafeKeyEnv {
			return "pixph_placeholder"
		}
		return ""
	}))
	if err != nil {
		t.Fatal(err)
	}
	d := cc.Decisions
	if d.Backend != decisions.BackendSystemOne || d.BaseURL != "https://api.typesafe.ai" || d.Model != "jev-latest" {
		t.Fatalf("decisions = %+v", d)
	}
	if k, err := d.Key(); err != nil || k != "pixph_placeholder" {
		t.Fatalf("key = %q, %v", k, err)
	}
	cc, err = run.ResolveClassifier(main, s.Classifier, run.EnvSource(func(string) string { return "" }))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cc.Decisions.Key(); err == nil {
		t.Fatal("a missing TYPESAFE_API_KEY was accepted")
	}
}

// The ApiKey comes from the environment as an ApiKey header, and an empty
// VULNETIX_API_TOKEN does not shadow it.
func TestPixCredentialIsTheEnvApiKey(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "VULNETIX_ORG_ID":
			return "11111111-2222-4333-8444-555555555555"
		case "VULNETIX_API_KEY":
			return "abc123"
		}
		return ""
	}
	got, err := vulnetixcreds.AuthHeader(env, t.TempDir(), "", nil)
	if err != nil || got != "ApiKey 11111111-2222-4333-8444-555555555555:abc123" {
		t.Fatalf("header = %q, %v", got, err)
	}
	// A token login is what the website refuses for remote control.
	withToken := func(k string) string {
		if k == "VULNETIX_API_TOKEN" {
			return "tok"
		}
		return env(k)
	}
	got, err = vulnetixcreds.AuthHeader(withToken, t.TempDir(), "", nil)
	if err != nil || !strings.HasPrefix(got, "Bearer ") {
		t.Fatalf("token header = %q, %v", got, err)
	}
}

// Everything secret the machine hands rc is invisible to a process tool, so a
// command a model runs cannot print the ApiKey, the Jev placeholder or the
// state directory. The org id is not a secret and passes.
func TestPixSecretsNeverReachToolEnvironments(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/pix",
		"TMPDIR=/workspace/tmp",
		"BELAI_HOME=/home/pix/.vulnetix/belai",
		"VULNETIX_ORG_ID=org",
		"VULNETIX_API_KEY=abc123",
		"VULNETIX_API_TOKEN=",
		"TYPESAFE_API_KEY=pixph_placeholder",
		"BELAI_NO_UPDATE_CHECK=1",
		"CI=1",
	}
	got := strings.Join(proc.ScrubEnvOf(env), "\n")
	for _, name := range []string{"VULNETIX_API_KEY", "VULNETIX_API_TOKEN", "TYPESAFE_API_KEY", "BELAI_HOME", "BELAI_NO_UPDATE_CHECK"} {
		if strings.Contains(got, name+"=") {
			t.Errorf("%s reached the tool environment", name)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/pix", "TMPDIR=/workspace/tmp", "VULNETIX_ORG_ID=org", "CI=1"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%s was scrubbed", keep)
		}
	}
	if strings.Contains(got, "abc123") || strings.Contains(got, "pixph_") {
		t.Error("a secret value survived the scrub")
	}
}

// Trust is exactly the --dir list: with the repositories cloned under a parent
// and the parent as the working directory, only the repositories are offered.
func TestPixTrustedDirsAreExactlyTheRepos(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	base, _ := Normalize(t.TempDir())
	root := filepath.Join(base, "workspace")
	repos := filepath.Join(root, "repos")
	a := filepath.Join(repos, "acme", "api")
	b := filepath.Join(repos, "acme", "web")
	for _, d := range []string{a, b, filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitRepo(t, a)
	gitRepo(t, b)

	offered, skipped := Collect([]string{a, b}, root)
	if len(skipped) != 0 || len(offered) != 2 {
		t.Fatalf("offered = %+v skipped = %+v", offered, skipped)
	}
	for _, d := range offered {
		if d.Source != SourceArg {
			t.Errorf("%s offered from %v, want --dir", d.Path, d.Source)
		}
	}
	for _, not := range []string{root, repos, filepath.Join(repos, "acme"), filepath.Join(root, "tmp")} {
		if _, ok := Allowed(offered, not); ok {
			t.Errorf("%s is offered but is not a repository", not)
		}
	}
	// A repository outside the list is not startable by the website's request.
	if _, ok := Allowed(offered, filepath.Join(repos, "acme")); ok {
		t.Error("a parent of a repository was allowed")
	}
}

// With every input in place preflight passes; with no repository yet it fails
// on directories alone, so rc is not started with nothing to trust.
func TestPixPreflight(t *testing.T) {
	pixHome(t)
	s, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	opts := PreflightOptions{
		Settings:   s,
		Dirs:       []Dir{{Path: "/workspace/repos/acme/api"}},
		LookPath:   func(string) (string, error) { return "/usr/bin/vulnetix", nil },
		AuthHeader: func(string) (string, error) { return "ApiKey org:abc123", nil },
	}
	if cs := Preflight(context.Background(), opts); !cs.OK() {
		t.Fatalf("preflight failed:\n%s", cs.Render())
	}

	opts.Dirs = nil
	cs := Preflight(context.Background(), opts)
	if cs.OK() {
		t.Fatal("preflight passed with no repository")
	}
	for _, c := range cs {
		if c.Level == Fail && c.Name != CheckDirs {
			t.Errorf("unexpected failure %s: %s", c.Name, c.Detail)
		}
	}

	// A token login fails: the machine empties VULNETIX_API_TOKEN for this reason.
	opts.Dirs = []Dir{{Path: "/x"}}
	opts.AuthHeader = func(string) (string, error) { return "Bearer tok", nil }
	if c, _ := Preflight(context.Background(), opts).Find(CheckCredential); c.Level != Fail {
		t.Errorf("token credential = %+v, want a failure", c)
	}

	// Guardrails off fails: rc never runs unwatched sessions without them.
	off := false
	opts.Settings.Guardrails = &off
	opts.AuthHeader = func(string) (string, error) { return "ApiKey org:abc123", nil }
	if c, _ := Preflight(context.Background(), opts).Find(CheckGuardrails); c.Level != Fail {
		t.Errorf("guardrails off = %+v, want a failure", c)
	}
}

// A missing vulnetix CLI is only a warning when the ApiKey resolves anyway.
func TestPixMissingCLIWithCredentialIsAWarning(t *testing.T) {
	cs := Preflight(context.Background(), PreflightOptions{
		Dirs:       []Dir{{Path: "/x"}},
		LookPath:   func(string) (string, error) { return "", errors.New("missing") },
		AuthHeader: func(string) (string, error) { return "ApiKey org:abc123", nil },
	})
	if c, _ := cs.Find(CheckCLI); c.Level != Warn {
		t.Fatalf("cli = %+v, want warn", c)
	}
}

// docs/pix-sandbox.md names the inputs it describes. The names it uses are the
// constants and keys the code reads, so a rename fails here first.
func TestPixDocNamesTheInputs(t *testing.T) {
	doc := docparity.Read(t, "docs/pix-sandbox.md")
	for _, want := range []string{
		"`$BELAI_HOME/sync/host-id`", "`$BELAI_HOME/settings.json`", "`VULNETIX_ORG_ID`", "`VULNETIX_API_KEY`", "`VULNETIX_API_TOKEN`",
		"`" + decisions.TypeSafeKeyEnv + "`", "`" + decisions.TypeSafeBaseURL + "`", "`" + decisions.TypeSafeDefaultModel + "`",
		"`" + decisions.TypeSafeProvider + "`", "`" + config.DefaultFirewall + "`", "`--dir <path>`",
		"`proc.ScrubbedEnv`", "`rc.Collect`", "`sessionsync.HostID`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/pix-sandbox.md lacks %s", want)
		}
	}
	// The settings the doc describes are valid JSON the settings loader accepts.
	var probe map[string]any
	if err := json.Unmarshal([]byte(pixSettings), &probe); err != nil {
		t.Fatal(err)
	}
	// The docs index lists the page, so a reader can find it.
	if idx := docparity.Read(t, "docs/README.md"); !strings.Contains(idx, "(pix-sandbox.md)") {
		t.Error("docs/README.md does not list pix-sandbox.md")
	}
}
