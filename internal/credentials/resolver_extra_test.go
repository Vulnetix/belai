package credentials

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/vulnetixcreds"
	"github.com/vulnetix/signet/internal/wire"
)

// mapEnv adapts a map to the Resolver's env accessor.
func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func newBareResolver(workdir string, env map[string]string, kc Keychain) *Resolver {
	return &Resolver{
		env:      mapEnv(env),
		workdir:  workdir,
		userFile: newFileStore(filepath.Join(workdir, "user.json"), false),
		projFile: newFileStore(filepath.Join(workdir, "proj.json"), true),
		netrc:    &netrcStore{path: filepath.Join(workdir, ".netrc")},
		keychain: kc,
	}
}

func TestUpperSnakeAndEnvVarForProvider(t *testing.T) {
	cases := map[string]string{
		"my-llm":         "SIGNET_MY_LLM_API_KEY",
		"My LLM":         "SIGNET_MY_LLM_API_KEY",
		"openai":         "SIGNET_OPENAI_API_KEY",
		"a.b-c_d e9f":    "SIGNET_A_B_C_D_E9F_API_KEY",
		"UPPER":          "SIGNET_UPPER_API_KEY",
		"Trailing-Slash": "SIGNET_TRAILING_SLASH_API_KEY",
		"trailing/slash": "SIGNET_TRAILING_SLASH_API_KEY",
		"has space":      "SIGNET_HAS_SPACE_API_KEY",
		"MiXeD case-99":  "SIGNET_MIXED_CASE_99_API_KEY",
	}
	for in, want := range cases {
		if got := EnvVarForProvider(in); got != want {
			t.Errorf("EnvVarForProvider(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFieldHost(t *testing.T) {
	if got := (Field{}).Host("openai"); got != "api.openai.com" {
		t.Errorf("Host(openai) = %q, want api.openai.com", got)
	}
	if got := (Field{}).Host("unknown-provider"); got != "" {
		t.Errorf("Host(unknown) = %q, want empty", got)
	}
}

func TestSetGetAndComplete(t *testing.T) {
	s := Set{
		Provider: "openai",
		Values: map[string]Value{
			"api_key": {Field: "api_key", Secret: true, value: "sk-1"},
			"org":     {Field: "org", Secret: false, value: "org-1"},
		},
	}
	if !s.Complete() {
		t.Fatal("set with no missing fields should be complete")
	}
	if got, ok := s.Get("api_key"); !ok || got != "sk-1" {
		t.Fatalf("Get(api_key) = (%q, %v)", got, ok)
	}
	if _, ok := s.Get("nope"); ok {
		t.Fatal("Get(nope) should miss")
	}
	s.Missing = []string{"org"}
	if s.Complete() {
		t.Fatal("set with missing fields should be incomplete")
	}
}

func TestSpecForCustomProfileOptionalKey(t *testing.T) {
	prof := &config.ProviderProfile{BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat}
	spec := SpecFor("my-llm", prof)
	if len(spec) != 1 || spec[0].Name != "api_key" || !spec[0].Optional {
		t.Fatalf("custom profile spec = %+v, want one optional api_key", spec)
	}
	// With no profile the derived name is mandatory and fail-closed.
	spec = SpecFor("my-llm", nil)
	if len(spec) != 1 || spec[0].Name != "api_key" || spec[0].Optional {
		t.Fatalf("nil profile spec = %+v, want one mandatory api_key", spec)
	}
}

func TestConfiguredProvidersAndProviderNames(t *testing.T) {
	env := map[string]string{
		"OPENAI_API_KEY":        "k",
		"SIGNET_MY_LLM_API_KEY": "k",
		"SIGNET_OTHER_API_KEY":  "k",
	}
	r := newBareResolver(t.TempDir(), env, &fakeKeychain{})
	r.settings = config.Settings{
		Providers: map[string]config.ProviderProfile{
			"my-llm": {BaseURL: "https://x.example/v1", API: wire.SurfaceOpenAIChat},
			"other":  {BaseURL: "https://y.example/v1", API: wire.SurfaceOpenAIChat},
		},
	}

	configured := r.ConfiguredProviders()
	joined := strings.Join(configured, ",")
	if !strings.Contains(joined, "openai") {
		t.Fatalf("configured providers %v missing openai", configured)
	}
	if !strings.Contains(joined, "my-llm") || !strings.Contains(joined, "other") {
		t.Fatalf("configured providers %v missing custom names", configured)
	}

	names := r.providerNames()
	// Custom names must be sorted after built-ins.
	if names[len(names)-2] != "my-llm" || names[len(names)-1] != "other" {
		t.Fatalf("custom names should be sorted last, tail = %v", names[len(names)-2:])
	}
}

func TestProfileAndLabelsAndCanonical(t *testing.T) {
	r := &Resolver{
		settings: config.Settings{
			Providers: map[string]config.ProviderProfile{
				"my-llm": {
					BaseURL: "https://x.example/v1",
					API:     wire.SurfaceOpenAIChat,
					Auth:    "x-api-key",
					Models:  []config.ProviderModel{{ID: "m1"}},
					Kind:    "openai-compatible",
				},
			},
			ProviderLabels: map[string]string{"my-llm": "My LLM"},
		},
	}

	p, ok := r.Profile("my-llm")
	if !ok {
		t.Fatal("Profile(my-llm) should be present")
	}
	if p.BaseURL != "https://x.example/v1" || p.Auth != "x-api-key" || len(p.Models) != 1 || p.Models[0] != "m1" || p.Kind != "openai-compatible" {
		t.Fatalf("Profile = %+v", p)
	}
	if _, ok := r.Profile("nope"); ok {
		t.Fatal("Profile(nope) should miss")
	}

	if got := r.Label("my-llm"); got != "My LLM" {
		t.Fatalf("Label = %q, want My LLM", got)
	}
	if got := r.Label("openai"); got != "openai" {
		t.Fatalf("Label(openai) = %q, want openai", got)
	}
	if got, ok := r.CanonicalProvider("My LLM"); !ok || got != "my-llm" {
		t.Fatalf("CanonicalProvider(My LLM) = (%q, %v)", got, ok)
	}
	if _, ok := r.CanonicalProvider("no such label"); ok {
		t.Fatal("CanonicalProvider should miss unknown label")
	}
}

func TestFirewallStateNoCredentialAndLoadError(t *testing.T) {
	// Empty credential: routable but missing cred yields ErrNoGatewayCredential.
	r := &Resolver{
		env:              mapEnv(nil),
		settings:         config.Settings{},
		vulnetixKeychain: &fakeKeychain{},
		keychain:         &fakeKeychain{},
	}
	r.vulnetixCredOnce.Do(func() {})
	r.vulnetixCred = vulnetixcreds.Credential{}
	st := r.FirewallState("anthropic")
	if !st.Routable || st.HasCred {
		t.Fatalf("state = %+v, want routable without cred", st)
	}
	if st.Reason == "" || !strings.Contains(st.Reason, "Firewall needs an API key") {
		t.Fatalf("reason = %q, want no-credential message", st.Reason)
	}

	// Load error propagates verbatim.
	r2 := &Resolver{
		env:              mapEnv(nil),
		settings:         config.Settings{},
		vulnetixKeychain: &fakeKeychain{},
		keychain:         &fakeKeychain{},
	}
	r2.vulnetixCredOnce.Do(func() {})
	r2.vulnetixCredErr = errors.New("load exploded")
	if got := r2.FirewallState("anthropic").Reason; got != "load exploded" {
		t.Fatalf("reason = %q, want load exploded", got)
	}
}

func TestFirewallStateHonorsSettingsGateway(t *testing.T) {
	gateway := "https://gw.custom.example"
	r := &Resolver{
		env:              mapEnv(nil),
		settings:         config.Settings{Vulnetix: &config.VulnetixSettings{GatewayURL: gateway}},
		vulnetixKeychain: &fakeKeychain{},
		keychain:         &fakeKeychain{},
	}
	r.vulnetixCredOnce.Do(func() {})
	r.vulnetixCred = vulnetixcreds.Credential{OrgUUID: "org-1", APIKey: "vk"}
	st := r.FirewallState("anthropic")
	if st.Gateway != gateway {
		t.Fatalf("Gateway = %q, want %q", st.Gateway, gateway)
	}
	if !strings.HasPrefix(st.BaseURL, gateway) {
		t.Fatalf("BaseURL = %q, want prefix %q", st.BaseURL, gateway)
	}
	if st.OrgUUID != "org-1" || st.APIKey != "vk" {
		t.Fatalf("state = %+v", st)
	}
}

func TestSetSettingsAndRefreshVulnetixCred(t *testing.T) {
	r := &Resolver{settings: config.Settings{Provider: "openai"}}
	r.SetSettings(config.Settings{Provider: "anthropic"})
	if r.settings.Provider != "anthropic" {
		t.Fatalf("SetSettings did not replace settings: %+v", r.settings)
	}
	r.RefreshVulnetixCred()
	// After refresh the once must fire again, so a subsequent load runs.
	var ran bool
	r.vulnetixCredOnce.Do(func() { ran = true })
	if !ran {
		t.Fatal("RefreshVulnetixCred did not reset the sync.Once")
	}
}

func TestResolveNetrcNotesPropagate(t *testing.T) {
	dir := t.TempDir()
	netrcPath := filepath.Join(dir, ".netrc")
	if err := os.WriteFile(netrcPath, []byte("machine api.openai.com password k\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newBareResolver(dir, map[string]string{}, &fakeKeychain{})
	r.netrc = &netrcStore{path: netrcPath}
	set := r.Resolve("openai")
	found := false
	for _, n := range set.Notes {
		if strings.Contains(n, "world-readable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected world-readable note, got %v", set.Notes)
	}
	if set.Values["api_key"].Reveal() != "k" {
		t.Fatalf("netrc api_key = %q", set.Values["api_key"].Reveal())
	}
}

func TestLookupKeychainAndFile(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	if err := os.WriteFile(userPath, []byte(`{"version":1,"providers":{"openai":{"api_key":{"source":"inline","value":"file-key"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// File wins over keychain.
	r := newBareResolver(dir, map[string]string{}, &fakeKeychain{data: map[string]string{"openai:api_key": "kc-key"}})
	r.userFile = newFileStore(userPath, false)
	v, origin, ok := r.Lookup("openai", "api_key")
	if !ok || v != "file-key" || origin != userPath {
		t.Fatalf("Lookup = (%q, %q, %v)", v, origin, ok)
	}

	// With no file, keychain resolves.
	r2 := newBareResolver(dir, map[string]string{}, &fakeKeychain{data: map[string]string{"openai:api_key": "kc-key"}})
	r2.userFile = newFileStore(filepath.Join(dir, "absent.json"), false)
	v, origin, ok = r2.Lookup("openai", "api_key")
	if !ok || v != "kc-key" || origin != "keychain" {
		t.Fatalf("keychain Lookup = (%q, %q, %v)", v, origin, ok)
	}

	// Unresolvable field misses.
	if _, _, ok := r2.Lookup("openai", "nope"); ok {
		t.Fatal("Lookup(nope) should miss")
	}
}

func TestStoreClearAndBackends(t *testing.T) {
	dir := t.TempDir()
	r := newBareResolver(dir, map[string]string{}, &fakeKeychain{data: map[string]string{}})

	// Store to user file.
	if err := r.Store("openai", "api_key", "stored", SourceUserFile); err != nil {
		t.Fatalf("Store user-file: %v", err)
	}
	if v, ok, _ := r.userFile.read("openai", "api_key", Spec("openai")); !ok || v.Reveal() != "stored" {
		t.Fatalf("user-file read = (%+v, %v)", v, ok)
	}

	// Store to project file.
	if err := r.Store("openai", "api_key", "proj", SourceProjectFile); err != nil {
		t.Fatalf("Store project-file: %v", err)
	}

	// Store to keychain.
	if err := r.Store("openai", "api_key", "kc", SourceKeychain); err != nil {
		t.Fatalf("Store keychain: %v", err)
	}
	kc := r.keychain.(*fakeKeychain)
	if kc.data["openai:api_key"] != "kc" {
		t.Fatalf("keychain store = %q, want kc", kc.data["openai:api_key"])
	}

	// Unsupported write backend.
	if err := r.Store("openai", "api_key", "x", SourceEnv); err == nil {
		t.Fatal("Store to env should fail")
	}

	// Clear from each backend.
	if err := r.Clear("openai", "api_key", SourceKeychain); err != nil {
		t.Fatalf("Clear keychain: %v", err)
	}
	if err := r.Clear("openai", "api_key", SourceUserFile); err != nil {
		t.Fatalf("Clear user-file: %v", err)
	}
	if err := r.Clear("openai", "api_key", SourceProjectFile); err != nil {
		t.Fatalf("Clear project-file: %v", err)
	}
	if err := r.Clear("openai", "api_key", SourceEnv); err == nil {
		t.Fatal("Clear from env should fail")
	}

	// Backends lists all five, sorted, with keychain unavailable reason for a
	// fake (non-keyringBackend) unavailable keychain.
	r2 := newBareResolver(dir, map[string]string{}, &fakeKeychain{broken: true})
	backends := r2.Backends()
	if len(backends) != 5 {
		t.Fatalf("Backends = %d, want 5", len(backends))
	}
	names := make([]string, len(backends))
	for i, b := range backends {
		names[i] = b.Name
	}
	want := []string{"env", "keychain", "netrc", "project-file", "user-file"}
	for i, w := range want {
		if names[i] != w {
			t.Fatalf("Backends order = %v, want %v", names, want)
		}
	}
	for _, b := range backends {
		if b.Name == "keychain" {
			if b.Available {
				t.Fatal("broken keychain should be unavailable")
			}
			if b.Reason != "not available" {
				t.Fatalf("keychain reason = %q, want not available", b.Reason)
			}
		}
	}
}

func TestStoreEnvRefRejectsNonFileBackends(t *testing.T) {
	r := newBareResolver(t.TempDir(), map[string]string{}, &fakeKeychain{})
	if err := r.StoreEnvRef("openai", "api_key", "GOOD_NAME", SourceNetrc); err == nil {
		t.Fatal("StoreEnvRef to netrc should fail")
	}
	if err := r.StoreEnvRef("openai", "api_key", "bad-name", SourceUserFile); err == nil {
		t.Fatal("StoreEnvRef with invalid name should fail")
	}
}
