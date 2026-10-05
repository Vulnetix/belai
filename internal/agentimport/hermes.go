package agentimport

import (
	"fmt"
	"strings"
)

// importHermes reads a Hermes profile: SOUL.md is the personality and system
// prompt, profile.yaml holds the description, config.yaml the settings, and
// skills/ the installed skills. auth.json, .env and the other credential stores
// are never read (see secretName), and the memories are the user's own and are
// left where they are.
func importHermes(t *tree, o Options) (Result, error) {
	soul, ok := t.text("SOUL.md")
	if !ok {
		return Result{}, fmt.Errorf("no SOUL.md in the source")
	}
	b := newBuilder(Hermes, t.base, o)
	profile := doc(nil)
	if data, ok := t.text("profile.yaml"); ok {
		d, err := parseDoc([]byte(data))
		if err != nil {
			return Result{}, fmt.Errorf("profile.yaml: %w", err)
		}
		profile = d
	}
	name := profile.str("name")
	if name == "" {
		name = t.base
	}
	b.setName(name)
	b.metaSet("source.name", name)
	b.setDescription(profile.str("description"), "Imported Hermes profile "+line(name, 60))
	b.setPrompt(soul)
	b.note(Mapped, "SOUL.md", "became the system prompt")

	if data, ok := t.text("distribution.yaml"); ok {
		if d, err := parseDoc([]byte(data)); err == nil {
			b.keep("distribution.yaml", "hermes.version", d.str("version"))
		}
	}
	if data, ok := t.text("config.yaml"); ok {
		cfg, err := parseDoc([]byte(data))
		if err != nil {
			return Result{}, fmt.Errorf("config.yaml: %w", err)
		}
		model, prov := hermesModel(cfg)
		if model != "" {
			b.setModel("config.yaml model", prov, model)
		}
		b.note(Dropped, "config.yaml", "settings Belai has no field for (%s) are not imported", strings.Join(capList(cfg.keys(), 12), ", "))
	}
	if t.has("mcp.json") {
		b.note(Dropped, "mcp.json", "MCP servers are the user's own settings in Belai and are never taken from a definition")
	}
	var cron, mem int
	for _, n := range t.names() {
		switch {
		case strings.HasPrefix(n, "cron/"):
			cron++
		case strings.HasPrefix(n, "memories/"):
			mem++
		}
	}
	if cron > 0 {
		b.note(Dropped, "cron/", "%d scheduled job file(s) are not imported", cron)
	}
	if mem > 0 {
		b.note(Dropped, "memories/", "%d memory file(s) are the user's own and are not imported", mem)
	}
	if t.skipped > 0 {
		b.note(Dropped, "files", "%d link, special or credential file(s) were not read", t.skipped)
	}
	b.collectSkills(t, "skills")
	return b.finish()
}

// hermesModel finds the model a Hermes config names: a plain `model` string, or
// a `model` mapping with `default` (or `name`) and an optional `provider`.
func hermesModel(cfg doc) (model, provider string) {
	if s := cfg.str("model"); s != "" {
		return s, cfg.str("provider")
	}
	m := cfg.sub("model")
	if m == nil {
		return "", ""
	}
	for _, k := range []string{"default", "name", "model"} {
		if s := m.str(k); s != "" {
			return s, m.str("provider")
		}
	}
	return "", ""
}
