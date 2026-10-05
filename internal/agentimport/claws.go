package agentimport

import (
	"fmt"
	"strings"
)

// importClaws reads a Claws package: a CLAW.md whose front matter declares the
// agent (id, model, tools, subagents, memory), its MCP servers and cron jobs,
// and whose body is the agent's instructions.
func importClaws(t *tree, o Options) (Result, error) {
	name := findFile(t, "CLAW.md")
	if name == "" {
		return Result{}, fmt.Errorf("no CLAW.md in the source")
	}
	text, _ := t.text(name)
	fm, body, err := frontMatter(text)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", name, err)
	}
	b := newBuilder(Claws, t.base, o)
	if v := fm.str("schemaVersion"); v != "" && v != "1" {
		b.note(Warning, "schemaVersion", "schema version %s is not the one this adapter was written for (1); fields it does not know are named below", line(v, 20))
	}
	agent := fm.sub("agent")
	id := agent.str("id")
	b.setName(id)
	if agent.str("name") != "" {
		b.keep("agent.name", "claws.name", agent.str("name"))
	}
	if id != "" {
		b.metaSet("source.name", id)
	}
	b.setDescription(agent.str("name"), "Imported Claws agent "+line(id, 60))

	prompt := body
	if prompt == "" {
		if src := fm.sub("workspace", "bootstrapFiles", "SOUL.md").str("source"); src != "" {
			if soul, ok := t.text(cleanRel(src)); ok {
				prompt = soul
				b.note(Mapped, "workspace.bootstrapFiles.SOUL.md", "used as the instructions")
			}
		}
	}
	if prompt == "" {
		if soul, ok := t.text("SOUL.md"); ok {
			prompt = soul
			b.note(Mapped, "SOUL.md", "used as the instructions")
		}
	}
	if body != "" {
		b.note(Mapped, "body", "the CLAW.md body became the system prompt")
	}
	b.setPrompt(prompt)

	model := agent.sub("model")
	if model != nil {
		b.setModel("agent.model.primary", "", model.str("primary"))
		if fb := model.strs("fallbacks"); len(fb) > 0 {
			b.keep("agent.model.fallbacks", "claws.model.fallbacks", strings.Join(fb, ", "))
		}
	}

	if tl := agent.sub("tools"); tl != nil {
		allow := append(tl.strs("allow"), tl.strs("alsoAllow")...)
		deny := tl.strs("deny")
		if len(allow) > 0 || len(deny) > 0 {
			b.addTools("agent.tools.allow", allow, deny)
		}
		if len(deny) > 0 {
			b.keep("agent.tools.deny", "claws.tools.deny", strings.Join(deny, ", "))
		}
		if fsd := tl.sub("fs"); fsd != nil {
			b.keep("agent.tools.fs.workspaceOnly", "claws.tools.fs.workspaceOnly", fsd.str("workspaceOnly"))
		}
	}
	if sub := agent.sub("subagents"); sub != nil {
		if l := sub.strs("allowAgents"); len(l) > 0 {
			b.keep("agent.subagents.allowAgents", "claws.subagents.allow", strings.Join(l, ", "))
		}
		b.keep("agent.subagents.delegationMode", "claws.subagents.mode", sub.str("delegationMode"))
	}
	if mem := agent.sub("memory", "search"); mem != nil {
		b.keep("agent.memory.search", "claws.memory.search", fmt.Sprintf("enabled=%s sources=%s", mem.str("enabled"), strings.Join(mem.strs("sources"), "+")))
	}
	if pk, ok := fm["packages"].([]any); ok && len(pk) > 0 {
		var l []string
		for _, x := range pk {
			if m, ok := x.(map[string]any); ok {
				d := doc(m)
				l = append(l, d.str("kind")+":"+d.str("ref")+"@"+d.str("version"))
			}
		}
		b.keep("packages", "claws.packages", strings.Join(capList(l, 12), ", "))
		b.note(Dropped, "packages", "Belai installs no package from a Claws manifest; the references are kept for you")
	}
	if mcp := fm.sub("mcpServers"); len(mcp) > 0 {
		b.note(Dropped, "mcpServers", "MCP servers (%s) are the user's own settings in Belai and are never taken from a definition", strings.Join(capList(mcp.keys(), 8), ", "))
	}
	if jobs, ok := fm["cronJobs"].([]any); ok && len(jobs) > 0 {
		for _, x := range jobs {
			if m, ok := x.(map[string]any); ok {
				d := doc(m)
				if id, cron := d.str("id"), d.sub("schedule").str("cron"); id != "" && cron != "" {
					b.metaSet("claws.cron."+profileName(id), cron)
				}
			}
		}
		b.note(Dropped, "cronJobs", "%d cron job(s) are not turned on; the schedules are kept in metadata, and a scheduled agent is something you set up yourself", len(jobs))
	}
	if ws := fm.sub("workspace"); ws != nil {
		b.note(Dropped, "workspace", "workspace files are not copied")
	}
	b.collectSkills(t, "skills")
	known := map[string]bool{"schemaVersion": true, "agent": true, "workspace": true, "packages": true, "mcpServers": true, "cronJobs": true}
	var unknown []string
	for _, k := range fm.keys() {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		b.note(Dropped, "front matter", "keys this adapter does not read: %s", strings.Join(capList(unknown, 10), ", "))
	}
	return b.finish()
}

// findFile returns the path in t whose base name is name (case-insensitive),
// preferring the shallowest.
func findFile(t *tree, name string) string {
	best := ""
	for _, n := range t.names() {
		base := n
		if i := strings.LastIndexByte(n, '/'); i >= 0 {
			base = n[i+1:]
		}
		if strings.EqualFold(base, name) && (best == "" || strings.Count(n, "/") < strings.Count(best, "/")) {
			best = n
		}
	}
	return best
}

// cleanRel normalises a package-relative path from a manifest for a lookup.
func cleanRel(p string) string {
	p = strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "./")
	return strings.TrimPrefix(p, "/")
}
