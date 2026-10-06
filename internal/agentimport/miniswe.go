package agentimport

import (
	"fmt"
	"strings"
)

// importMiniSWE reads a mini-SWE-agent YAML configuration: agent templates,
// a step limit, a model name and an execution environment. The agent works
// through a shell, so the profile gets reading tools and Bash, and Belai's own
// sandbox and permission rules replace the environment block.
func importMiniSWE(t *tree, o Options) (Result, error) {
	name := pickYAML(t, isMiniSWEDoc)
	if name == "" {
		return Result{}, fmt.Errorf("no mini-SWE-agent configuration (an agent block with system_template, or a model block with model_name) in the source")
	}
	data, _ := t.text(name)
	d, err := parseDoc([]byte(data))
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", name, err)
	}
	b := newBuilder(MiniSWE, t.base, o)
	b.setName(t.base)
	b.metaSet("source.name", t.base)
	b.setDescription("", "Imported mini-SWE-agent configuration "+line(t.base, 60))

	agent := d.sub("agent")
	prompt := agent.str("system_template")
	if inst := agent.str("instance_template"); inst != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += "How a task is given to you (a template from the source configuration):\n\n" + inst
	}
	b.setPrompt(prompt)
	if strings.Contains(prompt, "{{") || strings.Contains(prompt, "{%") {
		b.note(Warning, "agent templates", "the templates hold {{ }} or {%% %%} placeholders; Belai does not fill them, so they read as written")
	}
	b.note(Mapped, "agent.system_template", "became the system prompt")
	if n := agent.num("step_limit"); n > 0 {
		if n > maxIterations {
			n = maxIterations
			b.note(Warning, "agent.step_limit", "cut to %d", maxIterations)
		}
		b.p.MaxIterations = n
		b.note(Mapped, "agent.step_limit", "max_iterations %d", n)
	}
	if c := agent.str("cost_limit"); c != "" {
		b.keep("agent.cost_limit", "mini-swe.cost_limit", c)
	}
	if m := agent.str("mode"); m != "" {
		b.keep("agent.mode", "mini-swe.mode", m)
	}
	if mn := d.sub("model").str("model_name"); mn != "" {
		b.setModel("model.model_name", "", mn)
	}
	if ec := d.sub("environment").str("environment_class"); ec != "" {
		b.keep("environment.environment_class", "mini-swe.environment_class", ec)
	}
	if env := d.sub("environment"); env != nil {
		b.note(Dropped, "environment", "Belai runs commands in its own sandbox under its permission rules, so the execution environment is not imported")
	}
	// The agent's only tool is a shell.
	b.addTools("tools", []string{"read", "grep", "glob", "bash"}, nil)
	b.note(Warning, "tools", "the source agent works through a shell, so the profile gets Bash; each command still asks under Belai's permission rules")
	b.noteSkipped(t)
	known := map[string]bool{"agent": true, "model": true, "environment": true}
	var rest []string
	for _, k := range d.keys() {
		if !known[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) > 0 {
		b.note(Dropped, "configuration", "blocks Belai has no use for: %s", strings.Join(rest, ", "))
	}
	return b.finish()
}

func isMiniSWEDoc(d doc) bool {
	if a := d.sub("agent"); a != nil && (a.str("system_template") != "" || a.str("instance_template") != "") {
		return true
	}
	return d.sub("model").str("model_name") != "" && d.sub("agent") != nil
}
