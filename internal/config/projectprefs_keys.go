package config

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Flat keys for ProjectPrefs, the form the website reads and edits through
// `belai rc --web-project-settings`. A key is either one of prefFixedKeys or a
// map key under lsp.languages., jev.jobs. or jev.thresholds. naming a known
// language, job or cut-off. Values are a boolean, one word from the key's
// fixed set, or a number in [0,1]; nothing else is accepted.

// prefKind is the value shape a key takes.
type prefKind int

const (
	prefBool prefKind = iota
	prefWord
	prefNumber
)

type prefKey struct {
	kind  prefKind
	words []string
	// origin is the Effective.Origin key the value is reported under.
	origin string
}

var prefFixedKeys = map[string]prefKey{
	"guardrails":           {kind: prefBool, origin: "guardrails"},
	"ask_permission":       {kind: prefBool, origin: "ask_permission"},
	"firewall_enabled":     {kind: prefBool, origin: "firewall"},
	"caveman":              {kind: prefBool, origin: "caveman"},
	"mode":                 {kind: prefWord, words: []string{"agent", "plan", "goal", "auto"}},
	"show_reasoning":       {kind: prefBool, origin: "ui"},
	"show_tool_calls":      {kind: prefBool, origin: "ui"},
	"show_edits":           {kind: prefBool, origin: "ui"},
	"show_internal_work":   {kind: prefWord, words: []string{"hidden", "decisions", "security", "all"}, origin: "ui"},
	"layout":               {kind: prefWord, words: []string{LayoutClean, LayoutChronological}, origin: "ui"},
	"auto_commit_per_task": {kind: prefBool, origin: "auto_commit_per_task"},
	"tests.post_end":       {kind: prefWord, words: []string{"off", "goal", "goal_plan", "session"}, origin: "tests"},
	"tests.on_fail":        {kind: prefWord, words: []string{"off", "diagnose", "fix"}, origin: "tests"},
	"lsp.enabled":          {kind: prefBool, origin: "lsp"},
}

// PrefKeys lists every flat preference key in a stable order.
func PrefKeys() []string {
	keys := make([]string, 0, len(prefFixedKeys)+len(KnownLSPLanguages)+len(JevJobs)+32)
	for k := range prefFixedKeys {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, id := range KnownLSPLanguages {
		keys = append(keys, "lsp.languages."+id)
	}
	for _, j := range JevJobs {
		keys = append(keys, "jev.jobs."+string(j))
	}
	for _, k := range JevThresholdKeys() {
		keys = append(keys, "jev.thresholds."+k)
	}
	return keys
}

// lookupPrefKey returns the shape of key, and false for an unknown key.
func lookupPrefKey(key string) (prefKey, bool) {
	if k, ok := prefFixedKeys[key]; ok {
		return k, true
	}
	switch {
	case strings.HasPrefix(key, "lsp.languages."):
		if slices.Contains(KnownLSPLanguages, strings.TrimPrefix(key, "lsp.languages.")) {
			return prefKey{kind: prefBool, origin: "lsp"}, true
		}
	case strings.HasPrefix(key, "jev.jobs."):
		if ValidJevJob(strings.TrimPrefix(key, "jev.jobs.")) {
			return prefKey{kind: prefBool, origin: "jev"}, true
		}
	case strings.HasPrefix(key, "jev.thresholds."):
		if slices.Contains(JevThresholdKeys(), strings.TrimPrefix(key, "jev.thresholds.")) {
			return prefKey{kind: prefNumber, origin: "jev"}, true
		}
	}
	return prefKey{}, false
}

// Flat returns the keys set in p.
func (p ProjectPrefs) Flat() map[string]any {
	out := map[string]any{}
	putB := func(k string, v *bool) {
		if v != nil {
			out[k] = *v
		}
	}
	putB("guardrails", p.Guardrails)
	putB("ask_permission", p.AskPermission)
	putB("firewall_enabled", p.FirewallEnabled)
	putB("caveman", p.Caveman)
	if p.Mode != "" {
		out["mode"] = p.Mode
	}
	putB("show_reasoning", p.ShowReasoning)
	putB("show_tool_calls", p.ShowToolCalls)
	putB("show_edits", p.ShowEdits)
	if p.ShowInternalWork != nil {
		out["show_internal_work"] = *p.ShowInternalWork
	}
	if p.Layout != nil {
		out["layout"] = *p.Layout
	}
	putB("auto_commit_per_task", p.AutoCommitPerTask)
	if p.Tests != nil {
		if p.Tests.PostEnd != "" {
			out["tests.post_end"] = p.Tests.PostEnd
		}
		if p.Tests.OnFail != "" {
			out["tests.on_fail"] = p.Tests.OnFail
		}
	}
	if p.LSP != nil {
		putB("lsp.enabled", p.LSP.Enabled)
		for id, on := range p.LSP.Languages {
			out["lsp.languages."+id] = on
		}
	}
	if p.Jev != nil {
		for j, on := range p.Jev.Jobs {
			out["jev.jobs."+j] = on
		}
		for _, k := range JevThresholdKeys() {
			if v, set, _ := p.Jev.Thresholds.Value(k); set {
				out["jev.thresholds."+k] = v
			}
		}
	}
	return out
}

// SetFlat sets one flat key. v is a decoded JSON value: a bool, a string or a
// float64, matching the key's shape.
func (p *ProjectPrefs) SetFlat(key string, v any) error {
	k, ok := lookupPrefKey(key)
	if !ok {
		return fmt.Errorf("%q is not a project preference", key)
	}
	var b bool
	var w string
	var n float64
	switch k.kind {
	case prefBool:
		bv, ok := v.(bool)
		if !ok {
			return fmt.Errorf("%s takes true or false", key)
		}
		b = bv
	case prefWord:
		sv, ok := v.(string)
		if !ok || !slices.Contains(k.words, sv) {
			return fmt.Errorf("%s takes one of %s", key, strings.Join(k.words, ", "))
		}
		w = sv
	case prefNumber:
		fv, ok := v.(float64)
		if !ok || math.IsNaN(fv) || fv < 0 || fv > 1 {
			return fmt.Errorf("%s takes a number between 0 and 1", key)
		}
		n = math.Round(fv*100) / 100
	}
	switch key {
	case "guardrails":
		p.Guardrails = &b
	case "ask_permission":
		p.AskPermission = &b
	case "firewall_enabled":
		p.FirewallEnabled = &b
	case "caveman":
		p.Caveman = &b
	case "mode":
		p.Mode = w
	case "show_reasoning":
		p.ShowReasoning = &b
	case "show_tool_calls":
		p.ShowToolCalls = &b
	case "show_edits":
		p.ShowEdits = &b
	case "show_internal_work":
		p.ShowInternalWork = &w
	case "layout":
		p.Layout = &w
	case "auto_commit_per_task":
		p.AutoCommitPerTask = &b
	case "tests.post_end", "tests.on_fail":
		if p.Tests == nil {
			p.Tests = &PrefsTests{}
		}
		if key == "tests.post_end" {
			p.Tests.PostEnd = w
		} else {
			p.Tests.OnFail = w
		}
	case "lsp.enabled":
		if p.LSP == nil {
			p.LSP = &PrefsLSP{}
		}
		p.LSP.Enabled = &b
	default:
		switch {
		case strings.HasPrefix(key, "lsp.languages."):
			if p.LSP == nil {
				p.LSP = &PrefsLSP{}
			}
			if p.LSP.Languages == nil {
				p.LSP.Languages = map[string]bool{}
			}
			p.LSP.Languages[strings.TrimPrefix(key, "lsp.languages.")] = b
		case strings.HasPrefix(key, "jev.jobs."):
			if p.Jev == nil {
				p.Jev = &PrefsJev{}
			}
			if p.Jev.Jobs == nil {
				p.Jev.Jobs = map[string]bool{}
			}
			p.Jev.Jobs[strings.TrimPrefix(key, "jev.jobs.")] = b
		case strings.HasPrefix(key, "jev.thresholds."):
			if p.Jev == nil {
				p.Jev = &PrefsJev{}
			}
			p.Jev.Thresholds = p.Jev.Thresholds.With(strings.TrimPrefix(key, "jev.thresholds."), &n)
		}
	}
	return nil
}

// UnsetFlat removes one flat key, so the next layer down decides it again.
func (p *ProjectPrefs) UnsetFlat(key string) error {
	if _, ok := lookupPrefKey(key); !ok {
		return fmt.Errorf("%q is not a project preference", key)
	}
	switch key {
	case "guardrails":
		p.Guardrails = nil
	case "ask_permission":
		p.AskPermission = nil
	case "firewall_enabled":
		p.FirewallEnabled = nil
	case "caveman":
		p.Caveman = nil
	case "mode":
		p.Mode = ""
	case "show_reasoning":
		p.ShowReasoning = nil
	case "show_tool_calls":
		p.ShowToolCalls = nil
	case "show_edits":
		p.ShowEdits = nil
	case "show_internal_work":
		p.ShowInternalWork = nil
	case "layout":
		p.Layout = nil
	case "auto_commit_per_task":
		p.AutoCommitPerTask = nil
	case "tests.post_end", "tests.on_fail":
		if p.Tests != nil {
			if key == "tests.post_end" {
				p.Tests.PostEnd = ""
			} else {
				p.Tests.OnFail = ""
			}
			if *p.Tests == (PrefsTests{}) {
				p.Tests = nil
			}
		}
	case "lsp.enabled":
		if p.LSP != nil {
			p.LSP.Enabled = nil
		}
	default:
		switch {
		case strings.HasPrefix(key, "lsp.languages."):
			if p.LSP != nil {
				delete(p.LSP.Languages, strings.TrimPrefix(key, "lsp.languages."))
			}
		case strings.HasPrefix(key, "jev.jobs."):
			if p.Jev != nil {
				delete(p.Jev.Jobs, strings.TrimPrefix(key, "jev.jobs."))
			}
		case strings.HasPrefix(key, "jev.thresholds."):
			if p.Jev != nil {
				p.Jev.Thresholds = p.Jev.Thresholds.With(strings.TrimPrefix(key, "jev.thresholds."), nil)
			}
		}
	}
	if p.LSP != nil && p.LSP.Enabled == nil && len(p.LSP.Languages) == 0 {
		p.LSP = nil
	}
	if p.Jev != nil && len(p.Jev.Jobs) == 0 && (p.Jev.Thresholds == nil || *p.Jev.Thresholds == (JevThresholdSettings{})) {
		p.Jev = nil
	}
	return nil
}

// EffectivePref is one key's resolved value and the layer it came from.
type EffectivePref struct {
	Value  any
	Origin Source
}

// EffectivePrefs reports every flat key's resolved value in eff, with the
// layer that set it (SourceDefault when none did).
func EffectivePrefs(eff Effective, mode string) map[string]EffectivePref {
	s := eff.Settings
	origin := func(k prefKey) Source {
		if src, ok := eff.Origin[k.origin]; ok && k.origin != "" {
			return src
		}
		return SourceDefault
	}
	out := map[string]EffectivePref{}
	for _, key := range PrefKeys() {
		k, _ := lookupPrefKey(key)
		var v any
		switch key {
		case "guardrails":
			v = s.GuardrailsEnabled()
		case "ask_permission":
			v = s.AskPermissionEnabled()
		case "firewall_enabled":
			v = s.FirewallEnabled()
		case "caveman":
			v = s.CavemanEnabled()
		case "mode":
			v = mode
		case "show_reasoning":
			v = s.ReasoningVisible()
		case "show_tool_calls":
			v = s.ToolCallsVisible()
		case "show_edits":
			v = s.EditsVisible()
		case "show_internal_work":
			v = s.InternalWorkLevel()
		case "layout":
			v = s.Layout()
		case "auto_commit_per_task":
			v = s.AutoCommitPerTaskEnabled()
		case "tests.post_end":
			v = s.TestsPostEnd()
		case "tests.on_fail":
			v = s.TestsOnFail()
		case "lsp.enabled":
			v = s.LSPEnabled()
		default:
			switch {
			case strings.HasPrefix(key, "lsp.languages."):
				on, explicit := s.LSPLanguageEnabled(strings.TrimPrefix(key, "lsp.languages."))
				v = on || !explicit
			case strings.HasPrefix(key, "jev.jobs."):
				v = s.JevJobSet(JevJob(strings.TrimPrefix(key, "jev.jobs.")))
			case strings.HasPrefix(key, "jev.thresholds."):
				var t *JevThresholdSettings
				if s.Jev != nil {
					t = s.Jev.Thresholds
				}
				v, _, _ = t.Value(strings.TrimPrefix(key, "jev.thresholds."))
			}
		}
		out[key] = EffectivePref{Value: v, Origin: origin(k)}
	}
	return out
}
