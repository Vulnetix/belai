package agentimport

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
)

// importAgentItem converts an agent. The four formats Import reads work as they
// do there. A harness's subagent file (Claude Code, opencode, Copilot and the
// like) is Markdown with front matter and the instructions as the body; it
// becomes a single-mode, supervised profile through the same builder, so the
// tool table, the read-only fallback and the validator all apply. Belai's own
// profile files (.json or .md) are read strictly and kept whole.
func importAgentItem(t *tree, f Format, o Options) (Result, error) {
	var res Result
	var err error
	switch {
	case f == Belai:
		res, err = importBelaiAgent(t)
	case isHarness(f):
		res, err = importHarnessAgent(t, f, o)
	default:
		if f == "" {
			if f, err = detect(t); err != nil {
				if name, _, oerr := t.only(); oerr == nil && strings.HasSuffix(strings.ToLower(name), ".md") {
					res, err = importHarnessAgent(t, GenericMD, o)
					break
				}
				return Result{}, err
			}
		}
		res, err = importTree(t, f, o)
	}
	if err != nil {
		return Result{}, err
	}
	doc, err := agentprofile.MarshalMarkdown(res.Profile)
	if err != nil {
		return Result{}, fmt.Errorf("the profile cannot be written: %w", err)
	}
	if _, err := agentprofile.ParseMarkdown(doc); err != nil {
		return Result{}, fmt.Errorf("the converted profile does not read back: %w", err)
	}
	res.Doc, res.SHA256, res.Name = doc, libitem.Hash(doc), res.Profile.Name
	res.Description = line(res.Profile.Description, 300)
	return res, nil
}

func importBelaiAgent(t *tree) (Result, error) {
	name, data, err := t.only()
	if err != nil {
		return Result{}, err
	}
	p, err := agentprofile.ParseFile(name, data)
	if err != nil {
		return Result{}, fmt.Errorf("not a valid agent profile: %w", err)
	}
	if err := libstore.UntrustedText([]byte(p.SystemPrompt)); err != nil {
		return Result{}, err
	}
	n := &noter{}
	n.add(Mapped, "profile", "a Belai agent profile, kept as it is")
	var mut []string
	for _, tl := range p.Tools {
		if mutatingTools[tl] {
			mut = append(mut, tl)
		}
	}
	return Result{Format: Belai, Profile: p, Notes: n.list, MutatingTools: mut}, nil
}

// importHarnessAgent reads a subagent file from another harness.
func importHarnessAgent(t *tree, f Format, o Options) (Result, error) {
	n := &noter{}
	_, text, err := readText(t, n)
	if err != nil {
		return Result{}, err
	}
	fm, body, hasFM := softFrontMatter(text)
	if body == "" {
		return Result{}, errors.New("the file holds no instructions to use as the system prompt")
	}
	if err := libstore.UntrustedText([]byte(body)); err != nil {
		return Result{}, err
	}
	b := newBuilder(f, t.base, o)
	b.notes = append(b.notes, n.list...)
	if !hasFM {
		b.note(Mapped, "front matter", "the file has none, so the name comes from the file and the description from its first line")
	}
	b.setName(firstOf(fm.str("name"), itemStem(t.base)))
	desc := fm.str("description")
	if desc == "" {
		desc = firstLine(body)
	}
	b.setDescription(desc, "Imported "+f.label()+" agent "+line(t.base, 60))
	b.setPrompt(body)
	b.note(Mapped, "body", "the file body became the system prompt")

	done := map[string]bool{"name": true, "description": true}
	for _, k := range fm.keys() {
		key := fmKey(k)
		if done[key] {
			continue
		}
		v := fm[k]
		switch key {
		case "tools":
			allow, deny := toolSets(v)
			b.addTools(k, allow, append(deny, fmList(fm[firstKey(fm, "disallowedtools", "disallowed-tools")])...))
			done["disallowedtools"], done["disallowed-tools"] = true, true
		case "disallowedtools", "disallowed-tools":
			// Read with the tools; alone it only says what to remove from nothing.
			if _, has := fm[firstKey(fm, "tools")]; !has {
				b.addTools(k, nil, fmList(v))
			}
		case "model":
			if m := fmText(v); m != "" && !strings.EqualFold(m, "inherit") {
				b.setModel(k, "", m)
			}
		case "skills":
			b.keep(k, string(f)+".skills", fmText(v))
		case "maxturns", "max-turns":
			var n int
			if _, err := fmt.Sscanf(fmText(v), "%d", &n); err == nil && n > 0 {
				b.p.MaxIterations = min(n, maxIterations)
				b.note(Mapped, k, "max_iterations %d", b.p.MaxIterations)
			}
		case "mode", "temperature":
			b.keep(k, string(f)+"."+key, fmText(v))
		default:
			b.note(Dropped, k, "not imported: %s", dropText(f, key))
		}
	}
	b.noteSkipped(t)
	return b.finish()
}

func dropText(f Format, key string) string {
	if why, ok := dropReasons[strings.ReplaceAll(key, "-", "")]; ok {
		return why
	}
	if why, ok := dropReasons[key]; ok {
		return why
	}
	return fmt.Sprintf("a %s key Belai has no field for", f.label())
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// firstKey returns the key of fm that folds to one of the wanted spellings.
func firstKey(fm doc, want ...string) string {
	for k := range fm {
		for _, w := range want {
			if fmKey(k) == w {
				return k
			}
		}
	}
	return ""
}

// toolSets reads a tools value: a comma-separated string, a list, or opencode's
// map of tool name to on or off.
func toolSets(v any) (allow, deny []string) {
	if m, ok := v.(map[string]any); ok {
		for _, k := range doc(m).keys() {
			if strings.EqualFold(fmText(m[k]), "false") {
				deny = append(deny, k)
			} else {
				allow = append(allow, k)
			}
		}
		return allow, deny
	}
	return fmList(v), nil
}
