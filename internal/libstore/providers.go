package libstore

import (
	"encoding/json"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

func init() { register(libitem.Provider, providerStore{}) }

// providerStore keeps a provider set where Belai reads it: `providers` (the custom
// providers) and `firewall` (the AI Firewall instances) in the user's own
// settings.json. A provider's API key is never in either: `api_key_env` names an
// environment variable and a firewall instance names the header that carries its
// key. Keys reach the credentials resolver only through a provider_keys_install
// request (internal/rc). The host has one configuration, so there is at most one
// local provider item, named by LocalName, and installing a document replaces the
// whole configuration. A project settings file's providers (which Belai ignores
// without an opt-in anyway) are never read or written.
type providerStore struct{}

func hasProviders(s config.Settings) bool {
	fw := s.Firewall
	return len(s.Providers) > 0 || (fw != nil && (fw.Enabled != nil || fw.Active != "" || len(fw.Instances) > 0))
}

func (providerStore) list() ([]Local, []Skipped, error) {
	s, err := config.LoadGlobal()
	if err != nil {
		return nil, nil, err
	}
	if !hasProviders(s) {
		return nil, nil, nil
	}
	name := LocalName(libitem.Provider)
	d, err := libitem.ProviderDocument(name, s)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, nil, err
	}
	it, err := libitem.Validate(libitem.Provider, b)
	if err != nil {
		return nil, []Skipped{{Kind: libitem.Provider, Name: name, Reason: err.Error()}}, nil
	}
	return []Local{local(libitem.Provider, it.Name, it.Doc)}, nil, nil
}

func (providerStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	d, err := libitem.ParseProvider(it.Doc)
	if err != nil {
		return Result{}, err
	}
	path, err := config.GlobalSettingsPath()
	if err != nil {
		return Result{}, err
	}
	replaced := false
	err = config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		if hasProviders(*s) {
			if !o.Overwrite {
				return ErrExists
			}
			replaced = true
		}
		s.Providers = nil
		if len(d.Providers) > 0 {
			s.Providers = d.Providers
		}
		s.Firewall = d.Firewall
		return validateWritten(*s)
	})
	if err != nil {
		return Result{}, err
	}
	if err := setLocalName(libitem.Provider, d.Name); err != nil {
		return Result{Replaced: replaced, Where: path}, err
	}
	return Result{Replaced: replaced, Where: path + " (providers, firewall)"}, nil
}

// validateWritten runs Belai's own validators over settings about to be saved, so
// an install never leaves a file Belai refuses to load: a provider the routing or
// classifier still names that the new set removed, say.
func validateWritten(s config.Settings) error {
	if err := config.ValidateSettings(s); err != nil {
		return refusal("the settings would not be valid after this install, so nothing was written: %s", err)
	}
	return nil
}
