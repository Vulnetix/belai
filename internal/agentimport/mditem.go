package agentimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/filelib"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/skills"
)

// readText returns the single file of the tree as clean text: valid UTF-8, no
// NUL, no byte order mark, line feeds only.
func readText(t *tree, n *noter) (file, text string, err error) {
	file, data, err := t.only()
	if err != nil {
		return "", "", err
	}
	if len(data) == 0 {
		return file, "", errors.New("the file is empty")
	}
	if !utf8.Valid(data) {
		return file, "", errors.New("the file is not valid UTF-8")
	}
	for _, b := range data {
		if b == 0 {
			return file, "", errors.New("the file holds a NUL byte")
		}
	}
	text = string(data)
	if rest, ok := strings.CutPrefix(text, "\xef\xbb\xbf"); ok {
		text = rest
		n.add(Warning, "file", "a byte order mark was removed from the start of the file")
	}
	return file, strings.ReplaceAll(text, "\r\n", "\n"), nil
}

var (
	skillNameStrip = regexp.MustCompile(`[^a-z0-9]+`)
	cmdNameStrip   = regexp.MustCompile(`[^a-z0-9._-]+`)
)

// itemName makes the name the kind carries from a source word, or "" when
// nothing usable is left.
func itemName(kind Kind, raw string) string {
	raw = strings.ToLower(line(raw, 200))
	var s string
	switch kind {
	case KindSkill:
		s = strings.Trim(skillNameStrip.ReplaceAllString(raw, "-"), "-")
		if len(s) > 64 {
			s = strings.Trim(s[:64], "-")
		}
	case KindPrompt:
		s, _ = filelib.Slug(raw)
		if len(s) > 64 {
			s = strings.Trim(s[:64], "-")
		}
	default:
		s = strings.Trim(cmdNameStrip.ReplaceAllString(raw, "-"), ".-_")
		if len(s) > 64 {
			s = strings.Trim(s[:64], ".-_")
		}
	}
	return s
}

// firstLine is the first line of text that says something, without Markdown
// heading, quote or list marks.
func firstLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>*-` "))
		if l != "" {
			return line(l, 300)
		}
	}
	return ""
}

// importMarkdown converts a command, prompt or skill file.
func importMarkdown(t *tree, kind Kind, f Format, o Options) (Result, error) {
	n := &noter{}
	file, text, err := readText(t, n)
	if err != nil {
		return Result{}, err
	}
	if f == "" {
		f = GenericMD
	}
	if f == Belai {
		return importBelaiMarkdown(t, kind, file, text, n, o)
	}
	fm, body, hasFM := softFrontMatter(text)
	changed := !hasFM
	if !hasFM {
		n.add(Mapped, "front matter", "the file has none, so Belai's was added")
	}
	if body == "" {
		return Result{}, errors.New("the file has no instructions after the front matter")
	}
	stem := itemStem(t.base)
	native := kind == KindSkill || kind == KindCommand

	// Name. A skill is named by its front matter (its folder is the fallback); a
	// command or prompt by its file.
	srcName := stem
	if kind == KindSkill && fm.str("name") != "" {
		srcName = fm.str("name")
	}
	if o.Name != "" {
		srcName = o.Name
	}
	name := itemName(kind, srcName)
	if name == "" {
		return Result{}, fmt.Errorf("no usable name can be made from %q", line(srcName, 60))
	}
	if name != srcName {
		n.add(Warning, "name", "%q became %q: a name is lower case letters, digits and hyphens", line(srcName, 60), name)
		changed = true
	}
	if g := fm.str("name"); g != "" && kind != KindSkill && itemName(kind, g) != name {
		n.add(Warning, "name", "the front matter names it %q; the file name %q is the name", line(g, 60), name)
		changed = true
	}
	if kind == KindSkill && o.Name == "" && itemName(kind, stem) != name {
		n.add(Warning, "name", "the folder is %q but the skill names itself %q", line(stem, 60), name)
	}
	if fm.str("name") == "" && hasFM {
		changed = true
	}
	n.add(Mapped, "name", "%s", name)

	// Description.
	desc := line(fm.str("description"), 1024)
	if desc == "" {
		desc = firstLine(body)
		if desc == "" {
			return Result{}, errors.New("there is no description and nothing to take one from")
		}
		n.add(Mapped, "description", "none in the file; the first line of the body is used")
		changed = true
	} else {
		n.add(Mapped, "description", "kept")
	}
	if kind == KindPrompt && len(desc) > libitem.MaxDescriptionBytes {
		desc = line(desc, libitem.MaxDescriptionBytes)
		n.add(Warning, "description", "cut to %d bytes, the most a prompt holds", libitem.MaxDescriptionBytes)
		changed = true
	}

	// Every other key.
	var (
		hint, license, compat string
		disable               bool
		order                 int
		enabled               = true
		meta                  = map[string]string{}
	)
	for _, k := range fm.keys() {
		key := fmKey(k)
		v := fm[k]
		switch key {
		case "name", "description":
		case "argument-hint", "argumenthint", "hint":
			switch kind {
			case KindCommand:
				hint = line(fmText(v), libitem.MaxCommandArgumentHint)
				n.add(Mapped, k, "the argument hint")
			case KindSkill:
				keepMeta(n, meta, k, "source.argument-hint", fmText(v), "")
				changed = true
			default:
				n.add(Dropped, k, "not imported: a prompt has no argument hint")
				changed = true
			}
		case "allowed-tools", "allowedtools", "tools":
			// Kept as text, never as a grant: the tools an item may use are the user's
			// permission rules and the profile's tool list, whatever a source harness
			// allowed its own commands.
			if native {
				keepMeta(n, meta, k, "source.allowed-tools", fmText(v), "; text only, Belai never grants a tool from an imported file")
			} else {
				n.add(Dropped, k, "not imported: a prompt has no tool list, and a file never grants a tool")
			}
			changed = true
		case "license":
			if native {
				license = line(fmText(v), 128)
				n.add(Mapped, k, "kept")
			} else {
				n.add(Dropped, k, "not imported: a prompt has no license field")
				changed = true
			}
		case "compatibility":
			if native {
				compat = line(fmText(v), 500)
				n.add(Mapped, k, "kept")
			} else {
				n.add(Dropped, k, "not imported: a prompt has no compatibility field")
				changed = true
			}
		case "metadata":
			if m, ok := v.(map[string]any); ok && native {
				for _, mk := range doc(m).keys() {
					if strings.HasPrefix(mk, skills.MetaPrefix) {
						n.add(Dropped, k+"."+mk, "not imported: keys starting %q are Belai's own", skills.MetaPrefix)
						changed = true
						continue
					}
					if len(meta) >= skills.MaxMetadataEntries {
						n.add(Dropped, k+"."+mk, "not imported: at most %d metadata entries", skills.MaxMetadataEntries)
						changed = true
						continue
					}
					if s := line(fmText(m[mk]), 1000); s != "" {
						meta[line(mk, skills.MaxMetadataKey)] = s
					}
				}
				n.add(Mapped, k, "kept")
			} else {
				n.add(Dropped, k, "not imported: a %s has no metadata map", kind)
				changed = true
			}
		case "disable-model-invocation", "disablemodelinvocation":
			if native {
				disable = strings.EqualFold(fmText(v), "true")
				n.add(Mapped, k, "kept")
			} else {
				n.add(Dropped, k, "not imported")
				changed = true
			}
		case "order", "enabled":
			if kind == KindPrompt {
				if key == "order" {
					fmt.Sscanf(fmText(v), "%d", &order)
				} else {
					enabled = !strings.EqualFold(fmText(v), "false")
				}
				n.add(Mapped, k, "kept")
			} else {
				n.add(Dropped, k, "not imported: a %s has no %s", kind, key)
				changed = true
			}
		default:
			dropNote(n, f, k)
			changed = true
		}
	}
	bodyNotes(n, f, body)

	// Compose.
	var doc []byte
	switch kind {
	case KindPrompt:
		doc, err = libitem.ComposePrompt(libitem.PromptDoc{Name: name, Description: desc, Order: order, Enabled: enabled, Body: body})
	default:
		doc, err = composeNative(kind, name, desc, hint, license, compat, disable, meta, body)
	}
	if err != nil {
		return Result{}, err
	}
	item, err := libitem.Validate(libitem.Kind(kind), doc)
	if err != nil {
		return Result{}, err
	}
	// A file that was already a document the library accepts keeps its own bytes.
	if !changed {
		if raw, rerr := libitem.Validate(libitem.Kind(kind), []byte(text)); rerr == nil && raw.Name == name {
			item = raw
		}
	}
	if err := libstore.UntrustedText(item.Doc); err != nil {
		return Result{}, err
	}
	return Result{Format: f, Name: item.Name, Doc: item.Doc, SHA256: item.SHA256, Converted: changed, Description: desc, Notes: n.list}, nil
}

// keepMeta holds a source value in metadata under key and says so.
func keepMeta(n *noter, meta map[string]string, field, key, val, why string) {
	val = line(val, 1000)
	if val == "" {
		return
	}
	if _, have := meta[key]; !have && len(meta) >= skills.MaxMetadataEntries {
		n.add(Dropped, field, "not kept: at most %d metadata entries", skills.MaxMetadataEntries)
		return
	}
	meta[key] = val
	n.add(Kept, field, "kept in metadata as %s%s", key, why)
}

// composeNative writes a skill or command document: front matter from the
// parts, then the body. Fields are written plain when the loader reads them back
// as written, and quoted when not.
func composeNative(kind Kind, name, desc, hint, license, compat string, disable bool, meta map[string]string, body string) ([]byte, error) {
	for _, quote := range []bool{false, true} {
		var b strings.Builder
		val := func(s string) string {
			if quote {
				q, _ := json.Marshal(s)
				return string(q)
			}
			return s
		}
		b.WriteString("---\nname: " + name + "\ndescription: " + val(desc) + "\n")
		if license != "" {
			b.WriteString("license: " + val(license) + "\n")
		}
		if compat != "" {
			b.WriteString("compatibility: " + val(compat) + "\n")
		}
		if hint != "" && kind == KindCommand {
			b.WriteString("argument-hint: " + val(hint) + "\n")
		}
		if disable {
			b.WriteString("disable-model-invocation: true\n")
		}
		if len(meta) > 0 {
			b.WriteString("metadata:\n")
			keys := make([]string, 0, len(meta))
			for k := range meta {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				kq, _ := json.Marshal(k)
				vq, _ := json.Marshal(meta[k])
				b.WriteString("  " + string(kq) + ": " + string(vq) + "\n")
			}
		}
		b.WriteString("---\n\n" + strings.TrimSpace(body) + "\n")
		out, err := libitem.CanonicalMarkdown([]byte(b.String()))
		if err != nil {
			return nil, err
		}
		var m *skills.Manifest
		if kind == KindCommand {
			m, err = skills.ValidateCommand(string(out))
		} else {
			m, err = skills.ValidateSkill(string(out))
		}
		if err != nil {
			if quote {
				return nil, err
			}
			continue
		}
		if m.Name == name && m.Description == desc && m.ArgumentHint == hint && m.License == license && m.Compatibility == compat {
			return out, nil
		}
		if quote {
			return nil, errors.New("the front matter does not read back as written")
		}
	}
	return nil, errors.New("the front matter does not read back as written")
}

// importBelaiMarkdown reads a command, skill or prompt from Belai's own layout:
// a command or skill is already the library's document; a prompt is a body in a
// file named <NNN>-<name>.md, with its order and state in the name.
func importBelaiMarkdown(t *tree, kind Kind, file, text string, n *noter, o Options) (Result, error) {
	if kind == KindPrompt {
		order, slug, enabled, ok := filelib.Spec{Ext: ".md"}.ParseFileName(file)
		if !ok {
			return Result{}, fmt.Errorf("%s is not named <NNN>-<name>.md", line(file, 80))
		}
		doc, err := libitem.ComposePrompt(libitem.PromptDoc{Name: slug, Order: order, Enabled: enabled, Body: strings.TrimRight(text, " \t\r\n")})
		if err != nil {
			return Result{}, err
		}
		if err := libstore.UntrustedText(doc); err != nil {
			return Result{}, err
		}
		n.add(Mapped, "prompt", "%s, order %d", slug, order)
		return Result{Format: Belai, Name: slug, Doc: doc, SHA256: libitem.Hash(doc), Description: firstLine(text), Notes: n.list}, nil
	}
	item, err := libitem.Validate(libitem.Kind(kind), []byte(text))
	if err != nil {
		return Result{}, err
	}
	if err := libstore.UntrustedText(item.Doc); err != nil {
		return Result{}, err
	}
	stem := itemStem(t.base)
	if kind == KindCommand && stem != item.Name && file != "SKILL.md" {
		n.add(Warning, "name", "the file is %s.md but the command is named %s; Belai loads it as %s", line(stem, 60), item.Name, line(stem, 60))
	}
	if kind == KindSkill && t.base != item.Name {
		n.add(Warning, "name", "the folder is %s but the skill is named %s", line(t.base, 60), item.Name)
	}
	n.add(Mapped, "document", "already a Belai %s", kind)
	desc := ""
	if kind == KindSkill {
		if d, err := libitem.ParseSkill(item.Doc); err == nil {
			desc = line(d.Description, 300)
		}
	} else if d, err := libitem.ParseCommand(item.Doc); err == nil {
		desc = line(d.Description, 300)
	}
	return Result{Format: Belai, Name: item.Name, Doc: item.Doc, SHA256: item.SHA256, Description: desc, Notes: n.list}, nil
}
