package vulnetixcreds

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeKeychain struct {
	data map[string]string
}

func (f *fakeKeychain) Name() string    { return "fake" }
func (f *fakeKeychain) Available() bool { return true }
func (f *fakeKeychain) Get(account string) (string, error) {
	v, ok := f.data[account]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}
func (f *fakeKeychain) Set(account, secret string) error { return nil }
func (f *fakeKeychain) Delete(account string) error      { return nil }

func TestLoadEnvAPIKey(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "VULNETIX_API_KEY":
			return "key-1"
		case "VULNETIX_ORG_ID":
			return "org-1"
		}
		return ""
	}
	c, err := Load(env, "", "", &fakeKeychain{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.OrgUUID != "org-1" || c.APIKey != "key-1" || c.Source != "$VULNETIX_API_KEY" {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestLoadEnvVVDSecret(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "VVD_ORG":
			return "org-2"
		case "VVD_SECRET":
			return "secret-2"
		}
		return ""
	}
	c, err := Load(env, "", "", &fakeKeychain{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := hmacKey("secret-2", "org-2")
	if c.OrgUUID != "org-2" || c.APIKey != want {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestLoadFileAPIKeyMethod(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"org_id":"org-3","api_key":"org-3:deadbeef","method":"apikey"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok, err := loadFile(filepath.Join(dir, "credentials.json"), &fakeKeychain{})
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if !ok {
		t.Fatal("expected credential found")
	}
	if c.OrgUUID != "org-3" || c.APIKey != "deadbeef" {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestLoadFileSigV4(t *testing.T) {
	dir := t.TempDir()
	org := "org-4"
	secret := "secret-4"
	data := []byte(`{"org_id":"` + org + `","secret":"` + secret + `","method":"sigv4"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok, err := loadFile(filepath.Join(dir, "credentials.json"), &fakeKeychain{})
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if !ok {
		t.Fatal("expected credential found")
	}
	want := hmacKey(secret, org)
	if c.APIKey != want {
		t.Fatalf("sigv4 key mismatch: got %q want %q", c.APIKey, want)
	}
}

func TestLoadFileTokenOnly(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"org_id":"org-5","token":"tok","method":"token"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := loadFile(filepath.Join(dir, "credentials.json"), &fakeKeychain{})
	if ok {
		t.Fatal("expected no credential")
	}
	if err != ErrNoGatewayCredential {
		t.Fatalf("expected ErrNoGatewayCredential, got %v", err)
	}
}

func TestLoadFileKeyring(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"org_id":"org-6","api_key_in_keyring":true,"method":"apikey"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	kc := &fakeKeychain{data: map[string]string{"apikey:org-6": "kr-key"}}
	c, ok, err := loadFile(filepath.Join(dir, "credentials.json"), kc)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}
	if !ok || c.APIKey != "kr-key" {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestVVDSecretMatchesGateway(t *testing.T) {
	// The gateway validates signatures with HMAC-SHA256(secret, org_id).
	// Assert our derivation matches the canonical form.
	org := "11111111-1111-1111-1111-111111111111"
	secret := "demo-secret"
	got := hmacKey(secret, org)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(org))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("hmac mismatch: %q vs %q", got, want)
	}
}

func TestLoadEnvAPIKeyWithoutOrgFallsThrough(t *testing.T) {
	// A key with no org id is incomplete; Load must not return a partial
	// credential and instead falls through to "no credential found".
	env := func(k string) string {
		if k == "VULNETIX_API_KEY" {
			return "key-1"
		}
		return ""
	}
	_, err := Load(env, t.TempDir(), "", &fakeKeychain{})
	if err == nil {
		t.Fatal("expected error when key present but org id missing")
	}
}

func TestLoadEnvVVDOrgWithoutSecretFallsThrough(t *testing.T) {
	env := func(k string) string {
		if k == "VVD_ORG" {
			return "org-2"
		}
		return ""
	}
	_, err := Load(env, t.TempDir(), "", &fakeKeychain{})
	if err == nil {
		t.Fatal("expected error when org present but secret missing")
	}
}

func TestLoadWorkdirCredentialsDir(t *testing.T) {
	workdir := t.TempDir()
	credDir := filepath.Join(workdir, ".vulnetix")
	if err := os.MkdirAll(credDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"org_id":"org-7","api_key":"org-7:key7","method":"apikey"}`)
	if err := os.WriteFile(filepath.Join(credDir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(func(string) string { return "" }, t.TempDir(), workdir, &fakeKeychain{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.OrgUUID != "org-7" || c.APIKey != "key7" {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestLoadCredentialsDirEnv(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"org_id":"org-8","api_key":"org-8:key8","method":"apikey"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "VULNETIX_CREDENTIALS_DIR" {
			return dir
		}
		return ""
	}
	c, err := Load(env, t.TempDir(), "", &fakeKeychain{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.APIKey != "key8" {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestLoadFileTokenWithKeyDerivesAPIKey(t *testing.T) {
	dir := t.TempDir()
	// A token credential that also carries an API key derives from the key.
	data := []byte(`{"org_id":"org-9","api_key":"org-9:tokkey","method":"token"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok, err := loadFile(filepath.Join(dir, "credentials.json"), &fakeKeychain{})
	if err != nil || !ok {
		t.Fatalf("loadFile = (%+v, %v, %v)", c, ok, err)
	}
	if c.APIKey != "tokkey" {
		t.Fatalf("unexpected credential: %+v", c)
	}
}

func TestLoadFileSigV4Keyring(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"org_id":"org-10","hmac_in_keyring":true,"method":"sigv4"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	kc := &fakeKeychain{data: map[string]string{"hmac-secret:org-10": "kr-secret"}}
	c, ok, err := loadFile(filepath.Join(dir, "credentials.json"), kc)
	if err != nil || !ok {
		t.Fatalf("loadFile = (%+v, %v, %v)", c, ok, err)
	}
	want := hmacKey("kr-secret", "org-10")
	if c.APIKey != want {
		t.Fatalf("sigv4 keyring key mismatch: got %q want %q", c.APIKey, want)
	}
}

func TestLoadFileBadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := loadFile(filepath.Join(dir, "credentials.json"), &fakeKeychain{})
	if ok || err == nil {
		t.Fatalf("loadFile bad json = (ok=%v, err=%v), want error", ok, err)
	}
}

func TestLoadFileKeyringMissing(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"org_id":"org-11","api_key_in_keyring":true,"method":"apikey"}`)
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := loadFile(filepath.Join(dir, "credentials.json"), &fakeKeychain{data: map[string]string{}})
	if ok || err == nil {
		t.Fatalf("loadFile missing keyring entry = (ok=%v, err=%v), want error", ok, err)
	}
}

func TestLoadFileMissing(t *testing.T) {
	_, ok, err := loadFile(filepath.Join(t.TempDir(), "nope.json"), &fakeKeychain{})
	if ok || err != nil {
		t.Fatalf("loadFile missing = (ok=%v, err=%v), want silent miss", ok, err)
	}
}
