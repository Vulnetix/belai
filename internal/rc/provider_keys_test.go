package rc

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// userFileStore is the production resolver with one difference: a key goes to the
// user's credentials file, never to a keychain a test machine might have, so the
// test touches only its own state directory.
type userFileStore struct{ *credentials.Resolver }

func (userFileStore) PreferredBackend() credentials.Source { return credentials.SourceUserFile }

func useRealStore(t *testing.T) {
	t.Helper()
	old := openKeyStore
	openKeyStore = func() (KeyStore, error) {
		r, err := credentials.NewGlobalResolver()
		if err != nil {
			return nil, err
		}
		return userFileStore{r}, nil
	}
	t.Cleanup(func() { openKeyStore = old })
}

// memStore records what a request tried to store, in memory.
type memStore struct {
	stored  map[string]string
	failFor map[string]error
	origin  map[string]string
}

func (m *memStore) Store(provider, field, secret string, _ credentials.Source) error {
	if err := m.failFor[provider]; err != nil {
		return err
	}
	if field != "api_key" {
		panic("a provider key is stored under the api_key field")
	}
	m.stored[provider] = secret
	return nil
}
func (m *memStore) PreferredBackend() credentials.Source { return credentials.SourceUserFile }
func (m *memStore) Clear(provider, field string, _ credentials.Source) error {
	if err := m.failFor[provider]; err != nil {
		return err
	}
	if field != "api_key" {
		panic("a provider key is cleared from the api_key field")
	}
	delete(m.stored, provider)
	return nil
}
func (m *memStore) Lookup(provider, _ string) (string, string, bool) {
	if o, ok := m.origin[provider]; ok {
		return "v", o, true
	}
	return "", "", false
}

func useMemStore(t *testing.T) *memStore {
	t.Helper()
	m := &memStore{stored: map[string]string{}, failFor: map[string]error{}, origin: map[string]string{}}
	old := openKeyStore
	openKeyStore = func() (KeyStore, error) { return m, nil }
	t.Cleanup(func() { openKeyStore = old })
	return m
}

func (h *itemHarness) keys(providers ...string) (string, string) {
	return h.run(sessionsync.Dispatch{Kind: "provider_keys_install", Providers: providers})
}

func (h *itemHarness) serveKeys(body string) {
	h.site.mu.Lock()
	defer h.site.mu.Unlock()
	h.site.keysStatus, h.site.keysBody = 0, body
}

func TestProviderKeysAreStoredByProviderAndNamedInTheAckBySlugOnly(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	h.serveKeys(`{"keys":[{"provider":"anthropic","key":"sk-ant-TOPSECRET1"},{"provider":"openai","key":"  sk-TOPSECRET2  "}],"missing":["groq"]}`)
	status, why := h.keys("anthropic", "openai", "groq")
	if status != sessionsync.DispatchStarted {
		t.Fatalf("%s %s", status, why)
	}
	if mem.stored["anthropic"] != "sk-ant-TOPSECRET1" || mem.stored["openai"] != "sk-TOPSECRET2" || len(mem.stored) != 2 {
		t.Fatalf("stored = %v", mem.stored)
	}
	if !strings.Contains(why, "stored keys for anthropic, openai") || !strings.Contains(why, "groq (the library holds no usable key for it)") {
		t.Errorf("report = %q", why)
	}
	// The request was for this dispatch, and only that.
	if h.site.keysCalls != 1 || h.site.keysQuery != "dispatch=d-provider_keys_install" {
		t.Errorf("keys asked %d times with ?%s", h.site.keysCalls, h.site.keysQuery)
	}
	// Nothing a person or the website can read carries a key.
	for _, text := range []string{h.log.String(), why} {
		if strings.Contains(text, "TOPSECRET") || strings.Contains(text, "sk-") {
			t.Errorf("a key reached %q", text)
		}
	}
	h.site.mu.Lock()
	ack := h.site.acks["d-provider_keys_install"]
	h.site.mu.Unlock()
	if strings.Contains(strings.Join(ack[:], "|"), "TOPSECRET") {
		t.Errorf("a key reached the ack: %v", ack)
	}
}

func TestProviderKeysAreRefusedWhatTheHostWillNotStore(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	// A custom provider this host knows, a built-in, one it does not know.
	if err := config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		s.Providers = map[string]config.ProviderProfile{"my-llm": {BaseURL: "https://llm.example.com", API: "openai-chat"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.serveKeys(`{"keys":[
		{"provider":"anthropic","key":"sk-line\nbreak"},
		{"provider":"openai","key":"sk-tab\u0007bell"},
		{"provider":"groq","key":"   "},
		{"provider":"my-llm","key":"sk-custom"},
		{"provider":"nobody-has-this","key":"sk-orphan"},
		{"provider":"unrequested","key":"sk-extra"}]}`)
	status, why := h.keys("anthropic", "openai", "groq", "my-llm", "nobody-has-this")
	if status != sessionsync.DispatchStarted {
		t.Fatalf("%s %s", status, why)
	}
	if len(mem.stored) != 1 || mem.stored["my-llm"] != "sk-custom" {
		t.Fatalf("stored = %v", mem.stored)
	}
	for _, slug := range []string{"anthropic", "openai", "groq"} {
		if !strings.Contains(why, slug+" (the key is not one this host will store)") {
			t.Errorf("%s: report = %q", slug, why)
		}
	}
	if !strings.Contains(why, "nobody-has-this (no provider of that name is configured here)") {
		t.Errorf("report = %q", why)
	}
	if _, ok := mem.stored["unrequested"]; ok {
		t.Error("a key the request did not name was stored")
	}
	for _, leak := range []string{"sk-custom", "sk-orphan", "sk-extra", "line"} {
		if strings.Contains(why+h.log.String(), leak) {
			t.Errorf("%q reached the report or the log", leak)
		}
	}
}

// A response with a key over the limit is refused whole: the library never sends one,
// so one is a sign something is wrong, and nothing is stored from it.
func TestProviderKeysAnOversizedKeyRefusesTheResponse(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	h.serveKeys(`{"keys":[{"provider":"anthropic","key":"sk-fine"},{"provider":"openai","key":"` + strings.Repeat("k", sessionsync.MaxProviderKeyBytes+1) + `"}]}`)
	status, why := h.keys("anthropic", "openai")
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "longer than 4096 bytes") || len(mem.stored) != 0 || strings.Contains(why, "sk-fine") {
		t.Fatalf("%s %q %v", status, why, mem.stored)
	}
}

func TestProviderKeysNothingStoredIsARefusal(t *testing.T) {
	h := newItemHarness(t)
	useMemStore(t)
	h.serveKeys(`{"keys":[],"missing":["openai"]}`)
	status, why := h.keys("openai")
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "no key was stored") || !strings.Contains(why, "openai (the library holds no usable key for it)") {
		t.Fatalf("%s %q", status, why)
	}
}

func TestProviderKeysRefusalsBeforeTheLibraryIsAsked(t *testing.T) {
	h := newItemHarness(t)
	useMemStore(t)
	cases := map[string]struct {
		providers []string
		want      string
	}{
		"no providers":        {nil, "from 1 to 16 providers"},
		"a path as a slug":    {[]string{"../x"}, "not a provider slug"},
		"an uppercase slug":   {[]string{"OpenAI"}, "not a provider slug"},
		"an empty slug":       {[]string{""}, "not a provider slug"},
		"17 providers":        {manySlugs(17), "from 1 to 16 providers"},
		"a key as a slug":     {[]string{"sk-ant-api03-abc DEF"}, "not a provider slug"},
		"a slug over 64 long": {[]string{strings.Repeat("a", 65)}, "not a provider slug"},
	}
	for name, c := range cases {
		status, why := h.keys(c.providers...)
		if status != sessionsync.DispatchRefused || !strings.Contains(why, c.want) {
			t.Errorf("%s: %s %q, want %q", name, status, why, c.want)
		}
	}
	if h.site.keysCalls != 0 {
		t.Errorf("the library was asked for keys %d times for a request the host refuses", h.site.keysCalls)
	}
	// 16 is the most.
	h.serveKeys(`{"keys":[]}`)
	if status, _ := h.keys(manySlugs(16)...); status != sessionsync.DispatchRefused || h.site.keysCalls != 1 {
		t.Errorf("16 providers: %s, asked %d", status, h.site.keysCalls)
	}
	// Repeats count once.
	h.serveKeys(`{"keys":[]}`)
	h.keys("openai", "openai", "openai")
}

func manySlugs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "p" + string(rune('a'+i))
	}
	return out
}

func TestProviderKeysAreRefusedWhileSyncProvidersIsOff(t *testing.T) {
	h := newItemHarness(t)
	useMemStore(t)
	h.on[libitem.Provider] = false
	h.serveKeys(`{"keys":[{"provider":"openai","key":"sk-x"}]}`)
	status, why := h.keys("openai")
	if status != sessionsync.DispatchRefused || !strings.Contains(why, "sync.providers is off") || h.site.keysCalls != 0 {
		t.Fatalf("%s %q (asked %d)", status, why, h.site.keysCalls)
	}
}

func TestProviderKeysMapsTheLibrarysRefusalsToReasons(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	for status, want := range map[int]string{
		404: "no longer has this request",
		409: "already gave out the keys",
		403: "only sends keys over TLS",
		502: "could not release the keys",
		503: "could not release the keys",
		500: "could not read the keys from the library",
	} {
		h.site.mu.Lock()
		h.site.keysStatus, h.site.keysBody = status, `{"error":"x","keys":[{"provider":"openai","key":"sk-LEAK"}]}`
		h.site.mu.Unlock()
		got, why := h.keys("openai")
		if got != sessionsync.DispatchRefused || !strings.Contains(why, want) || strings.Contains(why, "LEAK") {
			t.Errorf("%d: %s %q, want one naming %q", status, got, why, want)
		}
	}
	if len(mem.stored) != 0 {
		t.Errorf("a failed request stored %v", mem.stored)
	}
}

func TestProviderKeyStoreFailureNeverEchoesTheKey(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	mem.failFor["openai"] = errors.New("keychain refused the value sk-TOPSECRET9 (too big)")
	h.serveKeys(`{"keys":[{"provider":"openai","key":"sk-TOPSECRET9"},{"provider":"anthropic","key":"sk-ant-ok"}]}`)
	status, why := h.keys("openai", "anthropic")
	if status != sessionsync.DispatchStarted || mem.stored["anthropic"] != "sk-ant-ok" {
		t.Fatalf("%s %q %v", status, why, mem.stored)
	}
	if strings.Contains(why+h.log.String(), "TOPSECRET9") || !strings.Contains(why, "openai (could not store it:") || !strings.Contains(why, "<redacted>") {
		t.Errorf("report = %q", why)
	}
}

func TestProviderKeysSaysWhenAnEnvironmentVariableStillWins(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	mem.origin["openai"] = "env $OPENAI_API_KEY"
	h.serveKeys(`{"keys":[{"provider":"openai","key":"sk-x"}]}`)
	_, why := h.keys("openai")
	if !strings.Contains(why, "openai (an environment variable still takes precedence)") {
		t.Errorf("report = %q", why)
	}
}

// With the real resolver: the key is stored under the provider's own name and
// resolves there, in the credentials file at 0600, and nowhere else on disk and in no
// text the daemon wrote.
func TestProviderKeysLandOnlyInTheCredentialsFile(t *testing.T) {
	h := newItemHarness(t)
	useRealStore(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("MY_LLM_KEY", "")
	if _, err := libstore.Install(libitem.Provider, []byte(`{"name":"mine","providers":{"my-llm":{"base_url":"https://llm.example.com/v1","api":"openai-chat","api_key_env":"MY_LLM_KEY"}}}`), libstore.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	const openai, custom = "sk-proj-REALLYSECRETKEY-aaaaaaaaaaaa", "llm-key-ALSOSECRET-bbbbbbbb"
	h.serveKeys(`{"keys":[{"provider":"openai","key":"` + openai + `"},{"provider":"my-llm","key":"` + custom + `"}]}`)
	status, why := h.keys("openai", "my-llm")
	if status != sessionsync.DispatchStarted || !strings.Contains(why, "stored keys for openai, my-llm") {
		t.Fatalf("%s %q", status, why)
	}

	// The resolver finds each where the provider's own configuration looks.
	r, err := credentials.NewGlobalResolver()
	if err != nil {
		t.Fatal(err)
	}
	for slug, want := range map[string]string{"openai": openai, "my-llm": custom} {
		got, ok := r.Resolve(slug).Get("api_key")
		if !ok || got != want {
			t.Errorf("%s resolves to %q (%v), want the stored key", slug, got, ok)
		}
	}

	// Every file under the state directory: only credentials.json holds a key.
	creds := filepath.Join(h.home, "credentials.json")
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(creds); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("credentials.json: %v %v", fi, err)
		}
	}
	err = filepath.WalkDir(h.home, func(path string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		holds := strings.Contains(string(b), "REALLYSECRETKEY") || strings.Contains(string(b), "ALSOSECRET")
		if holds && path != creds {
			t.Errorf("%s holds a key", path)
		}
		if !holds && path == creds {
			t.Errorf("credentials.json holds no key")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// And settings.json names the variable, never the key.
	s, _ := os.ReadFile(filepath.Join(h.home, "settings.json"))
	if !strings.Contains(string(s), "MY_LLM_KEY") {
		t.Errorf("settings.json = %s", s)
	}
	// Nothing the daemon logged or acknowledged carries one.
	h.site.mu.Lock()
	ack := h.site.acks["d-provider_keys_install"]
	h.site.mu.Unlock()
	b, _ := json.Marshal([]any{why, h.log.String(), ack})
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "sk-proj") {
		t.Errorf("a key reached the log or the ack: %s", b)
	}
}

// The audit record of a request is its kind and outcome: a provider key request is
// named, and nothing more.
func TestProviderKeyRequestsAreAuditedByKindOnly(t *testing.T) {
	h := newItemHarness(t)
	useMemStore(t)
	h.serveKeys(`{"keys":[{"provider":"openai","key":"sk-AUDITSECRET"}]}`)
	if status, _ := h.keys("openai"); status != sessionsync.DispatchStarted {
		t.Fatal(status)
	}
	// The audit stream, if one is running, lives in the state directory: no key in it.
	err := filepath.WalkDir(h.home, func(path string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		if b, _ := os.ReadFile(path); strings.Contains(string(b), "AUDITSECRET") {
			t.Errorf("%s holds a key", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (h *itemHarness) removeKeys(providers ...string) (string, string) {
	return h.run(sessionsync.Dispatch{Kind: "provider_keys_remove", Providers: providers})
}

// A remove request clears what an install stored, asks the library nothing, and
// names slugs only.
func TestProviderKeysRemoveClearsTheCredentialsFile(t *testing.T) {
	h := newItemHarness(t)
	useRealStore(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	const openai = "sk-proj-REMOVESECRET-aaaaaaaaaaaa"
	h.serveKeys(`{"keys":[{"provider":"openai","key":"` + openai + `"},{"provider":"anthropic","key":"sk-ant-KEEPSECRET"}]}`)
	if status, why := h.keys("openai", "anthropic"); status != sessionsync.DispatchStarted {
		t.Fatalf("%s %q", status, why)
	}
	calls := h.site.keysCalls

	status, why := h.removeKeys("openai", "openai")
	if status != sessionsync.DispatchStarted || why != "cleared keys for openai" {
		t.Fatalf("%s %q", status, why)
	}
	if h.site.keysCalls != calls {
		t.Error("a remove request asked the library for keys")
	}
	r, err := credentials.NewGlobalResolver()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Resolve("openai").Get("api_key"); ok {
		t.Error("openai still resolves a key")
	}
	if got, ok := r.Resolve("anthropic").Get("api_key"); !ok || got != "sk-ant-KEEPSECRET" {
		t.Error("anthropic lost its key")
	}
	b, _ := json.Marshal([]any{why, h.log.String()})
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("a key reached the log or the ack: %s", b)
	}
}

func TestProviderKeysRemoveSaysWhatItCouldNotClear(t *testing.T) {
	h := newItemHarness(t)
	mem := useMemStore(t)
	mem.stored["groq"] = "k"
	mem.origin["openai"] = "env $OPENAI_API_KEY"
	mem.failFor["mistral"] = errors.New("disk full")
	status, why := h.removeKeys("groq", "openai", "mistral")
	if status != sessionsync.DispatchStarted {
		t.Fatalf("%s %q", status, why)
	}
	if !strings.Contains(why, "cleared keys for groq, openai (an environment variable still supplies one)") || !strings.Contains(why, "mistral (could not clear it: disk full)") {
		t.Errorf("report = %q", why)
	}
	if _, ok := mem.stored["groq"]; ok {
		t.Error("groq was not cleared")
	}
	if status, why := h.removeKeys("Not A Slug"); status != sessionsync.DispatchRefused || why != "that is not a provider slug" {
		t.Errorf("bad slug: %s %q", status, why)
	}
	if status, _ := h.removeKeys(); status != sessionsync.DispatchRefused {
		t.Errorf("no slugs: %s", status)
	}
}
