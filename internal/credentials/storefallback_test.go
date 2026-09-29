package credentials

import (
	"errors"
	"strings"
	"testing"
)

// smallKeychain refuses a secret over limit bytes, as the macOS keychain
// refuses one over about 3000.
type smallKeychain struct {
	fakeKeychain
	limit int
}

func (k *smallKeychain) Set(account, secret string) error {
	if len(secret) > k.limit {
		return ErrTooBig
	}
	return k.fakeKeychain.Set(account, secret)
}

// A sign-in too large for the keychain Belai picked lands in the user file,
// and the caller is told where; a small one still goes to the keychain.
func TestStoreFallbackToUserFile(t *testing.T) {
	kc := &smallKeychain{fakeKeychain: fakeKeychain{data: map[string]string{}}, limit: 100}
	r := newBareResolver(t.TempDir(), nil, kc)

	got, err := r.StoreFallback("kiro", "login", "small", SourceKeychain)
	if err != nil || got != SourceKeychain || kc.data["kiro:login"] != "small" {
		t.Fatalf("small: %v %v %v", got, err, kc.data)
	}
	big := strings.Repeat("x", 4000)
	got, err = r.StoreFallback("kiro", "login", big, SourceKeychain)
	if err != nil || got != SourceUserFile {
		t.Fatalf("big: %v %v", got, err)
	}
	v, ok := r.Resolve("kiro").Values["login"]
	if !ok || v.Reveal() != big || v.Source != SourceUserFile {
		t.Fatalf("resolved %+v", v)
	}
}

// A backend the user named is never swapped: Store reports the refusal.
func TestStoreKeepsANamedKeychain(t *testing.T) {
	kc := &smallKeychain{fakeKeychain: fakeKeychain{data: map[string]string{}}, limit: 100}
	r := newBareResolver(t.TempDir(), nil, kc)
	if err := r.Store("kiro", "login", strings.Repeat("x", 4000), SourceKeychain); !errors.Is(err, ErrTooBig) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := r.Resolve("kiro").Values["login"]; ok {
		t.Fatal("secret written somewhere the user did not name")
	}
}
