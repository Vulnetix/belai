package rc

import (
	"os"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// LocalModels says what a web-started session can run on here: the model a
// request that names none gets (the same order `belai rc-session` resolves
// it in), and the providers this host holds credentials for with the models
// each offers. It reads the static catalogue only; a live /models fetch per
// heartbeat would be a network call per provider. Names only, never a key.
func LocalModels() *sessionsync.RCModels {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	settings, err := config.LoadMerged(wd)
	if err != nil {
		return nil
	}
	r, err := credentials.NewResolver(wd)
	if err != nil {
		return nil
	}
	return buildModels(settings, config.LoadState, os.Getenv, r.ConfiguredProviders())
}

// overrideOK is what a provider, model or effort named by a request may look
// like: an identifier, not a flag or a path. Model ids carry '/', '@', ':'
// and '.', for example "@cf/moonshotai/kimi-k2.6".
func overrideOK(s string) bool {
	if len(s) > 200 || strings.HasPrefix(s, "-") {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune("._:/@+-", c):
		default:
			return false
		}
	}
	return true
}

// checkOverride refuses a start request whose provider, model or effort this
// host cannot honour, and returns the reason; "" means go ahead. A model on
// its own (no provider) belongs to the host's default provider. The provider
// must be one this host holds credentials for: the website validated it
// against the advertisement, but the host decides.
func checkOverride(prov, model, effort string, models func() *sessionsync.RCModels) string {
	if prov == "" && model == "" && effort == "" {
		return ""
	}
	for _, v := range []string{prov, model, effort} {
		if !overrideOK(v) {
			return "the model choice in that request is not valid"
		}
	}
	if prov == "" {
		return ""
	}
	if models != nil {
		if m := models(); m != nil {
			for _, p := range m.Providers {
				if p.Name == strings.ToLower(prov) {
					return ""
				}
			}
		}
	}
	return "this host has no credentials for provider " + prov
}

// buildModels is LocalModels with its inputs passed in.
func buildModels(s config.Settings, loadState func() (config.State, error), env func(string) string, configured []string) *sessionsync.RCModels {
	prov, model := config.SelectedModel(s, loadState)
	prov = strings.ToLower(strings.TrimSpace(prov))
	if prov == "" {
		prov = strings.ToLower(strings.TrimSpace(env("BELAI_PROVIDER")))
	}
	if prov == "" {
		prov = strings.ToLower(strings.TrimSpace(env("PI_PROVIDER")))
	}
	if prov == "" {
		prov = "openai"
	}
	if model == "" {
		model = run.DefaultModel(prov)
	}
	out := &sessionsync.RCModels{
		Default: sessionsync.RCModelDefault{
			Provider: prov, Model: model,
			Routed: s.Routing != nil && s.Routing.Kind == config.RoutingRouted,
		},
		Providers: []sessionsync.RCProvider{},
	}
	saved, _ := loadState()
	names := append([]string{}, configured...)
	sort.Strings(names)
	for _, name := range names {
		if len(out.Providers) >= sessionsync.MaxRCProviders {
			break
		}
		p := sessionsync.RCProvider{Name: name, Models: []string{}}
		add := func(id string) {
			id = strings.TrimSpace(id)
			if id == "" || len(p.Models) >= sessionsync.MaxRCModelsPerProvider {
				return
			}
			for _, have := range p.Models {
				if have == id {
					return
				}
			}
			p.Models = append(p.Models, id)
		}
		if d, ok := provider.Lookup(name); ok {
			add(d.DefaultModel)
			for _, m := range d.Models {
				add(m.ID)
			}
		}
		// What the user last picked on this host belongs in its list even
		// when the static catalogue has never heard of it.
		if name == prov {
			add(model)
		}
		if saved.Provider == name {
			add(saved.Model)
		}
		out.Providers = append(out.Providers, p)
	}
	return out
}
