package rc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Provider keys (docs/library-items.md#provider-keys). A website user stores a key
// per provider with the AI Firewall (BYOK); the library holds it encrypted and
// never shows it. To put keys on this host the website sends a
// provider_keys_install request that names catalogue slugs and nothing else. The
// daemon then asks the library for the keys of that request, over TLS, once, and
// stores each in the credentials resolver under the provider's own name, where the
// provider's `api_key_env` or its built-in variable already resolves it.
//
// A key goes only to the resolver (the keychain when there is one, else the user's
// credentials file at 0600). It is never written to settings.json, a log, an
// acknowledgement, an audit event, the session record or an error, and every error
// that could mention one is redacted. A key that is empty, over 4096 bytes or holds
// a control character is refused, as the library refuses to store one.

// KeyStore is where a provider key lands. *credentials.Resolver is the one in
// production.
type KeyStore interface {
	// Store writes a credential to a backend.
	Store(provider, field, secret string, backend credentials.Source) error
	// PreferredBackend is the backend a harness-obtained secret goes to.
	PreferredBackend() credentials.Source
	// Lookup resolves a credential, to say where it comes from.
	Lookup(provider, field string) (value, origin string, ok bool)
}

// openKeyStore opens the store for the user's own layers (credentials.NewGlobalResolver)
// unless a test replaces it.
var openKeyStore = func() (KeyStore, error) { return credentials.NewGlobalResolver() }

var providerSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validProviderKey is the library's validateProviderKey: the key trimmed, not
// empty, at most sessionsync.MaxProviderKeyBytes, and no control character.
func validProviderKey(raw string) (string, bool) {
	key := strings.TrimSpace(raw)
	if key == "" || len(key) > sessionsync.MaxProviderKeyBytes {
		return "", false
	}
	if strings.ContainsFunc(key, unicode.IsControl) {
		return "", false
	}
	return key, true
}

// installProviderKeys answers a provider_keys_install request. It returns the report
// for the acknowledgement (provider slugs only), or the reason it refused.
func (d *Daemon) installProviderKeys(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	if !d.o.SyncItem(libitem.Provider) {
		return "", "sync.providers is off on this host, so it takes no provider key requests"
	}
	var slugs []string
	seen := map[string]bool{}
	for _, s := range r.Providers {
		if !providerSlug.MatchString(s) {
			return "", "that is not a provider slug"
		}
		if !seen[s] {
			seen[s] = true
			slugs = append(slugs, s)
		}
	}
	if len(slugs) == 0 || len(slugs) > sessionsync.MaxProviderKeys {
		return "", fmt.Sprintf("a request names from 1 to %d providers", sessionsync.MaxProviderKeys)
	}
	got, missing, err := d.o.Client.ProviderKeys(ctx, d.o.HostID, r.ID)
	switch {
	case errors.Is(err, sessionsync.ErrNotFound):
		return "", "the library no longer has this request for this host, so no key was sent"
	case errors.Is(err, sessionsync.ErrConflict):
		return "", "the library already gave out the keys for this request; ask for them again"
	case errors.Is(err, sessionsync.ErrKeysNotOverTLS):
		return "", "the library only sends keys over TLS, and this connection did not look like TLS to it"
	case errors.Is(err, sessionsync.ErrKeysUnavailable):
		return "", "the library could not release the keys (its key storage is unavailable or could not read one)"
	case err != nil:
		return "", "could not read the keys from the library: " + reason(err.Error())
	}
	byProvider := map[string]sessionsync.ProviderKey{}
	for _, k := range got {
		byProvider[k.Provider] = k
	}
	absent := map[string]bool{}
	for _, m := range missing {
		absent[m] = true
	}
	store, err := openKeyStore()
	if err != nil {
		return "", "could not open the credential store: " + reason(err.Error())
	}
	settings, _ := config.LoadGlobal()
	known := func(slug string) bool {
		_, custom := settings.Providers[slug]
		return provider.Builtin(slug) || custom
	}

	var stored, skipped []string
	for _, slug := range slugs {
		k, ok := byProvider[slug]
		switch {
		case !ok && absent[slug]:
			skipped = append(skipped, slug+" (the library holds no usable key for it)")
		case !ok:
			skipped = append(skipped, slug+" (the library sent no key for it)")
		case !known(slug):
			skipped = append(skipped, slug+" (no provider of that name is configured here)")
		default:
			key, valid := validProviderKey(k.Reveal())
			if !valid {
				skipped = append(skipped, slug+" (the key is not one this host will store)")
				continue
			}
			if err := store.Store(slug, "api_key", key, store.PreferredBackend()); err != nil {
				skipped = append(skipped, slug+" (could not store it: "+reason(credentials.Redact(err.Error(), []string{key}))+")")
				continue
			}
			note := ""
			if _, origin, found := store.Lookup(slug, "api_key"); found && strings.HasPrefix(origin, "env ") {
				note = " (an environment variable still takes precedence)"
			}
			stored = append(stored, slug+note)
		}
	}
	for i := range skipped {
		skipped[i] = sanitize.Line(skipped[i], 160)
	}
	if len(stored) == 0 {
		return "", "no key was stored: " + strings.Join(skipped, "; ")
	}
	report := "stored keys for " + strings.Join(stored, ", ")
	if len(skipped) > 0 {
		report += "; not stored: " + strings.Join(skipped, "; ")
	}
	return report, ""
}
