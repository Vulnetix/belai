package agentimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agentfiles"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filelib"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
	"github.com/vulnetix/belai/internal/locate"
)

// importCrew reads a crew file: a pure decode, with the shape checks of
// agentprofile.Crew.Validate that need no profile on this machine. Whether the
// members exist here is for the host that installs the crew.
func importCrew(t *tree, o Options) (Result, error) {
	_, data, err := t.only()
	if err != nil {
		return Result{}, err
	}
	var c agentprofile.Crew
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Result{}, fmt.Errorf("not a crew document: %w", err)
	}
	if dec.More() {
		return Result{}, errors.New("not a crew document: data after the JSON object")
	}
	c.Builtin = false
	if o.Name != "" {
		c.Name = o.Name
	}
	if profileName(c.Name) != c.Name || c.Name == "" {
		return Result{}, fmt.Errorf("the crew name %q is not lower case letters, digits, dot, underscore and hyphen", line(c.Name, 60))
	}
	if len(c.Members) == 0 || len(c.Members) > agentprofile.MaxCrewMembers {
		return Result{}, fmt.Errorf("a crew has 1 to %d members", agentprofile.MaxCrewMembers)
	}
	if c.ID != "" && !agentprofile.ValidID(c.ID) {
		return Result{}, fmt.Errorf("the crew id %q is not a lowercase UUID", line(c.ID, 40))
	}
	n := &noter{}
	for i, m := range c.Members {
		if m.Replicas < 0 || m.Replicas > agentprofile.MaxReplicas {
			return Result{}, fmt.Errorf("member %d: replicas run 1 to %d", i+1, agentprofile.MaxReplicas)
		}
		if strings.TrimSpace(m.Profile) == "" {
			n.add(Warning, fmt.Sprintf("members[%d]", i), "the member names no agent, so the crew cannot start until it does")
			continue
		}
		if line(m.Profile, 128) != m.Profile {
			return Result{}, fmt.Errorf("member %d: the agent name holds a control character", i+1)
		}
	}
	n.add(Mapped, "crew", "%s, %d member(s), %d worker(s)", c.Name, len(c.Members), c.Workers())
	doc, err := c.CanonicalJSON()
	if err != nil {
		return Result{}, err
	}
	return Result{Format: Belai, Name: c.Name, Doc: doc, SHA256: libitem.Hash(doc), Description: line(c.Description, 300), Notes: n.list}, nil
}

// importProcess reads a process file named <NNN>-<name>.json or .sh.
func importProcess(t *tree, o Options) (Result, error) {
	file, data, err := t.only()
	if err != nil {
		return Result{}, err
	}
	order, slug, enabled, ext, ok := filelib.Spec{Ext: ".sh", AltExts: []string{".json"}}.ParseFileNameExt(file)
	if !ok {
		return Result{}, fmt.Errorf("%s is not named <NNN>-<name>.json or .sh", line(file, 80))
	}
	if !utf8.Valid(data) {
		return Result{}, errors.New("the file is not valid UTF-8")
	}
	n := &noter{}
	var item libitem.Item
	if ext == ".json" {
		item, err = libitem.NormalizeProcessFile(data, slug, order, enabled)
		if err != nil {
			return Result{}, err
		}
	} else {
		d, err := libitem.LegacyProcess(slug, strings.TrimRight(string(data), "\n"), order, enabled)
		if err != nil {
			return Result{}, errors.New("this shell command cannot be a library item: " + err.Error())
		}
		b, err := json.Marshal(d)
		if err != nil {
			return Result{}, err
		}
		if item, err = libitem.Validate(libitem.Process, b); err != nil {
			return Result{}, err
		}
		n.add(Mapped, "file", "a shell file is offered as one `sh -c` command")
	}
	n.add(Mapped, "process", "%s, order %d", slug, order)
	return Result{Format: Belai, Name: item.Name, Doc: item.Doc, SHA256: item.SHA256, Notes: n.list, Converted: ext == ".sh"}, nil
}

// SettingsItem is one library item held in a settings file.
type SettingsItem struct {
	Kind Kind
	Name string
}

func decodeSettings(t *tree) (config.Settings, error) {
	_, data, err := t.only()
	if err != nil {
		return config.Settings{}, err
	}
	var s config.Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return config.Settings{}, errors.New("the settings file is not valid JSON")
	}
	return s, nil
}

func settingsHas(s config.Settings, kind Kind) bool {
	switch kind {
	case KindBudget:
		return len(s.TokenBudgets) > 0 || (s.UI != nil && (s.UI.BudgetCycleSeconds != nil || s.UI.BudgetWarn != nil))
	case KindRewrite:
		b := s.BashRewrite
		return b != nil && (len(b.Rules) > 0 || b.Enabled != nil)
	case KindProvider:
		fw := s.Firewall
		return len(s.Providers) > 0 || (fw != nil && (fw.Enabled != nil || fw.Active != "" || len(fw.Instances) > 0))
	}
	return false
}

// ListSettings names the library items a settings file holds: its repositories
// (one each), and its budget, rewrite and provider sets (one each, under the
// name the host gives that configuration).
func ListSettings(path string) ([]SettingsItem, error) {
	t, err := loadItem(path, KindBudget)
	if err != nil {
		return nil, err
	}
	s, err := decodeSettings(t)
	if err != nil {
		return nil, err
	}
	var out []SettingsItem
	for _, k := range []Kind{KindBudget, KindRewrite, KindProvider} {
		if settingsHas(s, k) {
			out = append(out, SettingsItem{Kind: k, Name: settingsName(k)})
		}
	}
	for _, r := range s.GitRepos {
		out = append(out, SettingsItem{Kind: KindRepo, Name: r.Name})
	}
	return out, nil
}

func settingsName(kind Kind) string {
	lk, _ := kind.Library()
	return libstore.LocalName(lk)
}

// importSettings builds the document of a budget, rewrite, provider or repository
// from a settings file. o.Name picks a repository; the sets have one document
// each.
func importSettings(t *tree, kind Kind, o Options) (Result, error) {
	s, err := decodeSettings(t)
	if err != nil {
		return Result{}, err
	}
	n := &noter{}
	var raw any
	name := o.Name
	switch kind {
	case KindRepo:
		if name == "" {
			return Result{}, errors.New("name the repository to read")
		}
		found := false
		for _, r := range s.GitRepos {
			if r.Name == name {
				raw, found = libitem.RepoDocument(r), true
				break
			}
		}
		if !found {
			return Result{}, fmt.Errorf("the settings file has no repository %s", line(name, 64))
		}
	case KindBudget, KindRewrite, KindProvider:
		if !settingsHas(s, kind) {
			return Result{}, fmt.Errorf("the settings file holds no %s configuration", kind)
		}
		if name == "" {
			name = settingsName(kind)
		}
		switch kind {
		case KindBudget:
			raw = libitem.BudgetDocument(name, s)
		case KindRewrite:
			raw, name = libitem.RewriteDocument(s), libitem.RewriteName
		case KindProvider:
			raw, err = libitem.ProviderDocument(name, s)
			if err != nil {
				return Result{}, err
			}
		}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Result{}, err
	}
	lk, _ := kind.Library()
	item, err := libitem.Validate(lk, b)
	if err != nil {
		return Result{}, err
	}
	n.add(Mapped, string(kind), "%s, read from %s", line(item.Name, 64), filepath.Base(firstFile(t)))
	if kind == KindProvider {
		n.add(Mapped, "keys", "no API key is in the document; a key is never read from settings")
	}
	return Result{Format: Belai, Name: item.Name, Doc: item.Doc, SHA256: item.SHA256, Notes: n.list}, nil
}

func firstFile(t *tree) string {
	n, _, _ := t.only()
	return n
}

// importDocument reads an instruction file (AGENTS.md, CLAUDE.md and the like)
// through the gates a profile's own files pass: UTF-8 text, no NUL, within the
// size limit, no credential-looking name, and no private key or known token.
func importDocument(t *tree, f Format, o Options) (Result, error) {
	n := &noter{}
	file, text, err := readText(t, n)
	if err != nil {
		return Result{}, err
	}
	if len(text) > agentfiles.MaxFileBytes {
		return Result{}, fmt.Errorf("the file is over %d bytes", agentfiles.MaxFileBytes)
	}
	// A hidden name is fine here (.cursorrules, .windsurfrules are instruction
	// files); a credential store, a binary type or a large file is not.
	if why, ok := locate.EligibleFile(file, int64(len(text))); !ok && why != locate.SkipHidden {
		return Result{}, errors.New("the file is not one an agent reads (a credential store, a binary type or too large)")
	}
	if agentfiles.HoldsSecret([]byte(text)) {
		return Result{}, errors.New("the file holds a private key or a known token, so it is not imported")
	}
	if err := libstore.UntrustedText([]byte(text)); err != nil {
		return Result{}, err
	}
	name := o.Name
	if name == "" {
		name = file
	}
	n.add(Mapped, "document", "%s, %d bytes", line(name, 80), len(text))
	doc := []byte(text)
	return Result{Format: f, Name: name, Doc: doc, SHA256: libitem.Hash(doc), Notes: n.list}, nil
}
