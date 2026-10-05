package agentimport

import (
	"fmt"
	"sort"
	"strings"
)

// importFabric reads an NVIDIA NeMo Fabric agent configuration (the
// FabricConfig of its SDK schema): metadata, named model roles, portable
// instructions, tool and skill capability blocks, MCP servers and runtime
// limits.
func importFabric(t *tree, o Options) (Result, error) {
	name := pickYAML(t, isFabricDoc)
	if name == "" {
		return Result{}, fmt.Errorf("no NeMo Fabric agent configuration (schema_version, metadata, runtime) in the source")
	}
	data, _ := t.text(name)
	d, err := parseDoc([]byte(data))
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", name, err)
	}
	b := newBuilder(Nemoclaw, t.base, o)
	md := d.sub("metadata")
	b.setName(md.str("name"))
	b.metaSet("source.name", md.str("name"))
	b.setDescription(md.str("description"), "Imported NeMo Fabric agent "+line(md.str("name"), 60))

	system := d.sub("instructions", "system")
	b.setPrompt(system.str("content"))
	if system.str("mode") == "append" {
		b.note(Warning, "instructions.system.mode", "the source appends to its harness's default instructions; Belai's own system prompt is separate, so the text stands alone here")
	}

	if models := d.sub("models"); len(models) > 0 {
		roles := models.keys()
		pick := roles[0]
		for _, pref := range []string{"default", "main", "primary", "agent"} {
			if _, ok := models[pref]; ok {
				pick = pref
				break
			}
		}
		m := models.sub(pick)
		b.setModel("models."+pick, m.str("provider"), m.str("model"))
		for _, r := range roles {
			if r == pick {
				continue
			}
			mm := models.sub(r)
			b.keep("models."+r, "fabric.models."+profileName(r), mm.str("provider")+"/"+mm.str("model"))
		}
		for _, r := range roles {
			mm := models.sub(r)
			if mm.str("base_url") != "" || mm.str("api_key_env") != "" {
				b.note(Dropped, "models."+r, "endpoint and credential references are never imported")
			}
		}
		if m != nil && (m.str("temperature") != "" || m.str("max_tokens") != "" || m.str("top_p") != "") {
			b.keep("models."+pick, "fabric.sampling", fmt.Sprintf("temperature=%s top_p=%s max_tokens=%s", m.str("temperature"), m.str("top_p"), m.str("max_tokens")))
		}
	}

	if tl := d.sub("tools"); tl != nil {
		_, hasEnabled := tl["enabled"]
		if hasEnabled || len(tl.strs("blocked")) > 0 {
			b.addTools("tools.enabled", tl.strs("enabled"), tl.strs("blocked"))
		}
		if defs := tl.sub("definitions"); len(defs) > 0 {
			b.note(Dropped, "tools.definitions", "tool definitions (%s) name code or factories and are never imported", strings.Join(capList(defs.keys(), 8), ", "))
		}
	}
	if rt := d.sub("runtime"); rt != nil {
		if n := rt.num("max_turns"); n > 0 {
			if n > maxIterations {
				n = maxIterations
				b.note(Warning, "runtime.max_turns", "cut to %d", maxIterations)
			}
			b.p.MaxIterations = n
			b.note(Mapped, "runtime.max_turns", "max_iterations %d", n)
		}
		if ts := rt.str("timeout_seconds"); ts != "" {
			b.keep("runtime.timeout_seconds", "fabric.timeout_seconds", ts)
		}
	}
	if mcp := d.sub("mcp", "servers"); len(mcp) > 0 {
		b.note(Dropped, "mcp.servers", "MCP servers (%s) are the user's own settings in Belai and are never taken from a definition", strings.Join(capList(mcp.keys(), 8), ", "))
	}
	if sk := d.sub("skills"); sk != nil {
		if paths := sk.strs("paths"); len(paths) > 0 {
			for _, p := range paths {
				root := strings.TrimSuffix(cleanRel(p), "/")
				if root != "" && !strings.Contains(root, "..") {
					b.collectSkills(t, root)
				}
			}
			if len(b.skills) == 0 {
				b.note(Dropped, "skills.paths", "no skill directory in the source holds a SKILL.md that Belai accepts")
			}
		}
	}
	known := map[string]bool{"schema_version": true, "metadata": true, "models": true, "instructions": true, "tools": true, "runtime": true, "mcp": true, "skills": true}
	var rest []string
	for _, k := range d.keys() {
		if !known[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	if len(rest) > 0 {
		b.note(Dropped, "configuration", "blocks Belai has no use for: %s", strings.Join(rest, ", "))
	}
	return b.finish()
}

func isFabricDoc(d doc) bool {
	_, a := d["schema_version"]
	_, b := d["metadata"]
	_, c := d["runtime"]
	return a && b && c
}

// pickYAML returns the one top-level .yaml, .yml or .json file of t that match
// accepts, or "" when there is none (or, ambiguously, more than one).
func pickYAML(t *tree, match func(doc) bool) string {
	var found []string
	for _, n := range t.names() {
		lower := strings.ToLower(n)
		if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".json")) {
			continue
		}
		if d, err := parseDoc(t.files[n]); err == nil && match(d) {
			found = append(found, n)
		}
	}
	if len(found) == 0 {
		return ""
	}
	sort.Slice(found, func(i, j int) bool {
		if di, dj := strings.Count(found[i], "/"), strings.Count(found[j], "/"); di != dj {
			return di < dj
		}
		return found[i] < found[j]
	})
	return found[0]
}
