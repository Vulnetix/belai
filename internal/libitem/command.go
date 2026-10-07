package libitem

import (
	"strings"

	"github.com/vulnetix/belai/internal/skills"
)

// MaxCommandArgumentHint is the longest argument-hint of a custom slash command,
// in bytes.
const MaxCommandArgumentHint = 256

func init() {
	register(Command, func(c []byte) (string, error) { d, err := ParseCommand(c); return d.Name, err }, nil)
}

// CommandDoc is a validated custom slash command: the name it is typed as
// (/name), a description, an optional hint of the arguments it takes, the tools
// it may narrow a turn to, and the template after the front matter.
type CommandDoc struct {
	Name         string
	Description  string
	ArgumentHint string
	AllowedTools []string
	Body         string
}

// ParseCommand validates a canonical command document: the front matter a skill
// has (name and description required; license, compatibility, metadata,
// allowed-tools and disable-model-invocation optional, each with the skill's
// limits) plus an optional argument-hint of at most MaxCommandArgumentHint
// bytes. Any other key is refused. A command has no reserved name and no
// belai.* metadata rules, which belong to skills, and its body must not be
// empty. These are vdb-site's belaiValidateCommand rules.
func ParseCommand(canonical []byte) (CommandDoc, error) {
	doc := string(canonical)
	if !strings.HasPrefix(doc, "---\n") {
		return CommandDoc{}, refuse("the document must open with a --- front matter line")
	}
	if err := strictClosing(doc); err != nil {
		return CommandDoc{}, err
	}
	m, err := skills.ValidateCommand(doc)
	if err != nil {
		return CommandDoc{}, refuse("%s", clip(err.Error(), 200))
	}
	var d CommandDoc
	d.Name = m.Name
	if !ValidName(Command, d.Name) {
		return d, refuse("name %q is not valid: %s", cleanForMessage(d.Name), nameRule)
	}
	d.Description = m.Description
	if len(d.Description) > MaxSkillDescriptionBytes {
		return d, refuse("description is %d bytes; the most is %d", len(d.Description), MaxSkillDescriptionBytes)
	}
	if len(m.License) > MaxSkillLicense {
		return d, refuse("license is over %d bytes", MaxSkillLicense)
	}
	if len(m.Compatibility) > MaxSkillCompatibility {
		return d, refuse("compatibility is over %d bytes", MaxSkillCompatibility)
	}
	if err := skills.CheckMetadataBounds(m.Metadata); err != nil {
		return d, refuse("%s", err.Error())
	}
	if len(m.AllowedTools) > MaxSkillAllowedTools {
		return d, refuse("allowed-tools has %d entries; the most is %d", len(m.AllowedTools), MaxSkillAllowedTools)
	}
	for _, t := range m.AllowedTools {
		if len(t) > MaxSkillTool {
			return d, refuse("an allowed-tools entry is over %d bytes", MaxSkillTool)
		}
	}
	if len(m.ArgumentHint) > MaxCommandArgumentHint {
		return d, refuse("argument-hint is %d bytes; the most is %d", len(m.ArgumentHint), MaxCommandArgumentHint)
	}
	d.ArgumentHint = m.ArgumentHint
	d.AllowedTools = m.AllowedTools
	_, body, err := skills.Split(doc)
	if err != nil {
		return d, refuse("%s", clip(err.Error(), 200))
	}
	if strings.TrimSpace(body) == "" {
		return d, refuse("the command has no instructions after the front matter")
	}
	d.Body = strings.TrimLeft(body, "\n")
	return d, nil
}

// ComposeCommand writes a command document: the name, the description and the
// argument-hint (each flattened to one line, the hint only when there is one),
// the allowed tools as a space-separated string when there are any, then the
// template. The result is canonical.
func ComposeCommand(d CommandDoc) ([]byte, error) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	var b strings.Builder
	b.WriteString("---\nname: " + d.Name + "\ndescription: " + flat(d.Description) + "\n")
	if h := flat(d.ArgumentHint); h != "" {
		b.WriteString("argument-hint: " + h + "\n")
	}
	if len(d.AllowedTools) > 0 {
		b.WriteString("allowed-tools: " + strings.Join(d.AllowedTools, " ") + "\n")
	}
	b.WriteString("---\n\n" + d.Body)
	out, err := CanonicalMarkdown([]byte(b.String()))
	if err != nil {
		return nil, err
	}
	if _, err := ParseCommand(out); err != nil {
		return nil, err
	}
	return out, nil
}
