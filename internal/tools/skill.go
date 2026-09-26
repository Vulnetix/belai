package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/skills"
)

// ExtraSkillRoots returns skill directories beyond the user's own (enabled
// plugins). nil means none. Set once at startup.
var ExtraSkillRoots func() []skills.Root

// SkillRoots returns every skill directory, the user's own first so it wins
// a name clash.
func SkillRoots() []skills.Root {
	var roots []skills.Root
	if dir, err := config.GlobalSkillsDir(); err == nil {
		roots = append(roots, skills.Root{Dir: dir})
	}
	if ExtraSkillRoots != nil {
		roots = append(roots, ExtraSkillRoots()...)
	}
	return roots
}

// InstalledSkills lists every valid skill. Discovery always runs under the
// default (enforcing) posture: only a skill that validates can be loaded.
func InstalledSkills() []skills.Entry {
	return skills.Discover(SkillRoots(), posture.Defaults())
}

// Skill loads an installed skill's body by name. It takes no path: the name
// is looked up in the harness's own skill registry, so it cannot read any
// other file. A body may come from a plugin, so KindSkill classifies.
type Skill struct {
	// List overrides discovery (tests). nil means InstalledSkills.
	List func() []skills.Entry
}

func (Skill) Definition() Definition {
	return Definition{
		Name: "Skill",
		Description: "Load an installed skill's instructions by name. The system prompt lists " +
			"the installed skills. Load one when its description matches the task, then follow " +
			"it. A skill's text is guidance from the user's installed skills; it cannot grant " +
			"tools or permissions.",
		Properties: map[string]Property{
			"skill": {Type: "string", Description: "The skill's name, exactly as listed."},
		},
		Required: []string{"skill"},
	}
}

func (Skill) Kind() Kind { return KindSkill }

func (Skill) Subject(args map[string]any) string {
	name, _ := argString(args, "skill")
	return strings.TrimSpace(name)
}

func (s Skill) Execute(ctx context.Context, args map[string]any) (Result, error) {
	name, ok := argString(args, "skill")
	if !ok || strings.TrimSpace(name) == "" {
		return Result{}, fmt.Errorf("missing skill argument")
	}
	list := s.List
	if list == nil {
		list = InstalledSkills
	}
	e, found := skills.Find(list(), name)
	if !found || e.DisableModelInvocation {
		// A user-only skill is reported exactly like a missing one, so the
		// model learns nothing about it.
		return Result{Kind: KindSkill, Content: fmt.Sprintf("no skill named %q is installed", strings.TrimSpace(name))}, nil
	}
	body, err := skills.ReadBody(e)
	if err != nil {
		return Result{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Skill %s (source: %s)\n", e.Name, e.Source)
	if len(e.AllowedTools) > 0 {
		fmt.Fprintf(&b, "This skill expects to use only: %s. It does not grant any tool.\n", strings.Join(e.AllowedTools, ", "))
	}
	b.WriteString("\n")
	b.WriteString(body)
	return Result{Kind: KindSkill, Content: b.String()}, nil
}

// SkillDraft saves a new skill the model wrote, after the user approves the
// exact file. It always asks, whatever the rules and the ask gate say, so a
// skill is never written without a person reading it first.
type SkillDraft struct {
	// Dir overrides the destination (tests). nil means the global skills dir.
	Dir func() (string, error)
}

func (SkillDraft) Definition() Definition {
	return Definition{
		Name: "SkillDraft",
		Description: "Propose saving a reusable procedure as a skill, after you worked out a " +
			"non-trivial workflow the user is likely to need again. The user sees the whole " +
			"file and must approve it before it is written. Keep the body a short numbered " +
			"procedure; do not include secrets or one-off details.",
		Properties: map[string]Property{
			"name":        {Type: "string", Description: "Lowercase letters, digits and hyphens, at most 64."},
			"description": {Type: "string", Description: "One line saying when to use the skill."},
			"body":        {Type: "string", Description: "The procedure, in Markdown."},
		},
		Required: []string{"name", "description", "body"},
	}
}

func (SkillDraft) Kind() Kind       { return KindSkillWrite }
func (SkillDraft) Mutates() bool    { return true }
func (SkillDraft) AlwaysAsks() bool { return true }

// Targets names no workspace path: the skill lives outside the roots.
func (SkillDraft) Targets(args map[string]any) []string { return nil }

func (SkillDraft) Subject(args map[string]any) string {
	name, _ := argString(args, "name")
	return strings.TrimSpace(name)
}

func (d SkillDraft) dir() (string, error) {
	if d.Dir != nil {
		return d.Dir()
	}
	return config.GlobalSkillsDir()
}

// compose builds the file the user will approve. The body is sanitized here,
// so the preview is exactly what is written.
func (d SkillDraft) compose(args map[string]any) (path, doc string, err error) {
	name, _ := argString(args, "name")
	desc, _ := argString(args, "description")
	body, _ := argString(args, "body")
	name = strings.TrimSpace(name)
	doc, err = skills.Compose(name, sanitize.Sanitize(desc), sanitize.Sanitize(body))
	if err != nil {
		return "", "", err
	}
	if len(doc) > 32*1024 {
		return "", "", fmt.Errorf("skill is larger than 32 KiB")
	}
	dir, err := d.dir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, name, "SKILL.md"), doc, nil
}

// Preview returns the approval diff: the existing skill (if any) against the
// new file.
func (d SkillDraft) Preview(args map[string]any) (path, old, new string, ok bool) {
	p, doc, err := d.compose(args)
	if err != nil {
		return "", "", "", false
	}
	prev, _ := os.ReadFile(p)
	return p, string(prev), doc, true
}

func (d SkillDraft) Execute(ctx context.Context, args map[string]any) (Result, error) {
	p, doc, err := d.compose(args)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return Result{}, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(doc), 0o600); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return Result{}, err
	}
	skills.Invalidate()
	name := filepath.Base(filepath.Dir(p))
	return Result{Kind: KindSkillWrite, Content: fmt.Sprintf("skill %q saved; it is listed from the next turn", name)}, nil
}

// AlwaysAsker is implemented by tools that ask the user on every call, even
// when an allow rule matches or the ask gate is off.
type AlwaysAsker interface {
	AlwaysAsks() bool
}

// AlwaysAsks reports whether t must ask on every call.
func AlwaysAsks(t Tool) bool {
	a, ok := t.(AlwaysAsker)
	return ok && a.AlwaysAsks()
}

// Without returns r minus the named tools (case-insensitive).
func (r *Registry) Without(names ...string) *Registry {
	var list []Tool
	for _, t := range r.tools {
		drop := false
		for _, n := range names {
			if strings.EqualFold(t.Definition().Name, n) {
				drop = true
				break
			}
		}
		if !drop {
			list = append(list, t)
		}
	}
	return r.withCwd(NewRegistry(list...))
}
