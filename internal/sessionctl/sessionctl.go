// Package sessionctl is the one vocabulary of session controls: the mode, the
// model, the posture switches and the display filters a person changes while a
// session runs. The TUI's slash commands and keys, a `belai rc --web-controls`
// session and the website's control strip all speak it, so a key pressed on the
// web means exactly what it means on the host.
//
// A control changes the session it is applied to and nothing else: State is
// overlaid on a copy of the settings (State.Apply) and is never written to a
// settings file. Every value is checked against a fixed table here or a
// validator the settings already use (Jev thresholds, Jev job names, language
// ids); a free-form command line never runs. Each change carries a summary the
// harness composes from the control's id and the canonical value, so no text a
// caller supplied is echoed into a transcript.
package sessionctl

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Control ids. They are the wire names of the website's control strip.
const (
	CtlMode       = "mode"
	CtlModel      = "model"
	CtlEffort     = "effort"
	CtlGuardrails = "guardrails"
	CtlAsk        = "ask"
	CtlCaveman    = "caveman"
	CtlAutoCommit = "autocommit"
	CtlReasoning  = "reasoning"
	CtlTools      = "tools"
	CtlDecisions  = "decisions"
	CtlTests      = "tests"
	CtlLSP        = "lsp"
	CtlJev        = "jev"
)

// Kinds of control, for the website's rendering.
const (
	KindToggle = "toggle"
	KindChoice = "choice"
	KindModel  = "model"
	KindNested = "nested"
)

// Control describes one control: its slash command, the keys bound to it, the
// values it takes and whether the agent session must be rebuilt for a change
// to take effect.
type Control struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Usage   string   `json:"usage"`
	Keys    []string `json:"keys,omitempty"`
	Values  []string `json:"values,omitempty"`
	Kind    string   `json:"kind"`
	Rebuild bool     `json:"rebuild"`
}

// Value sets, in the order a key cycles them.
var (
	Modes = []string{"agent", "plan", "goal", "code", "auto"}
	// CycleModes is what the mode key walks through. Code mode is chosen by name
	// (/mode code), never cycled into.
	CycleModes     = []string{"agent", "plan", "goal", "auto"}
	ReasoningModes = []string{"auto", "shown", "hidden"}
	ToolModes      = []string{"auto", "all", "edits", "none"}
	DecisionLevels = []string{"hidden", "decisions", "security", "all"}
	TestsPostEnd   = []string{"off", "goal", "goal_plan", "session"}
	TestsOnFail    = []string{"off", "diagnose", "fix"}
	onOff          = []string{"on", "off"}
)

// Controls is the catalogue, in the order the website shows it.
var Controls = []Control{
	{ID: CtlMode, Command: "/mode", Usage: "/mode agent|plan|goal|code|auto", Keys: []string{"shift+tab", "f5"}, Values: Modes, Kind: KindChoice},
	{ID: CtlModel, Command: "/model", Usage: "/model <provider> <model> [effort]", Keys: []string{"ctrl+q"}, Kind: KindModel, Rebuild: true},
	{ID: CtlEffort, Command: "/effort", Usage: "/effort default|<level>", Keys: []string{"f6"}, Kind: KindChoice, Rebuild: true},
	{ID: CtlGuardrails, Command: "/guardrails", Usage: "/guardrails on|off", Keys: []string{"f3"}, Values: onOff, Kind: KindToggle, Rebuild: true},
	{ID: CtlAsk, Command: "/ask", Usage: "/ask on|off", Keys: []string{"f4"}, Values: onOff, Kind: KindToggle, Rebuild: true},
	{ID: CtlCaveman, Command: "/caveman", Usage: "/caveman on|off", Keys: []string{"f2"}, Values: onOff, Kind: KindToggle, Rebuild: true},
	{ID: CtlAutoCommit, Command: "/autocommit", Usage: "/autocommit on|off", Values: onOff, Kind: KindToggle},
	{ID: CtlReasoning, Command: "/reasoning", Usage: "/reasoning auto|shown|hidden", Keys: []string{"ctrl+r"}, Values: ReasoningModes, Kind: KindChoice},
	{ID: CtlTools, Command: "/tools", Usage: "/tools auto|all|edits|none", Keys: []string{"ctrl+t"}, Values: ToolModes, Kind: KindChoice},
	{ID: CtlDecisions, Command: "/decisions", Usage: "/decisions hidden|decisions|security|all", Values: DecisionLevels, Kind: KindChoice},
	{ID: CtlTests, Command: "/tests", Usage: "/tests post_end off|goal|goal_plan|session · /tests on_fail off|diagnose|fix", Kind: KindNested},
	{ID: CtlLSP, Command: "/lsp", Usage: "/lsp on|off [language]", Values: onOff, Kind: KindNested, Rebuild: true},
	{ID: CtlJev, Command: "/jev", Usage: "/jev job <name> on|off · /jev threshold <key> <0..1> · /jev reset", Kind: KindNested, Rebuild: true},
}

// Lookup returns the control with id.
func Lookup(id string) (Control, bool) {
	for _, c := range Controls {
		if c.ID == id {
			return c, true
		}
	}
	return Control{}, false
}

// commandControl maps a slash command to its control.
func commandControl(cmd string) (Control, bool) {
	for _, c := range Controls {
		if c.Command == cmd {
			return c, true
		}
	}
	return Control{}, false
}

// IsCommand reports whether line names a control's slash command, so a caller
// can tell a control line from prompt text that happens to start with "/".
func IsCommand(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	_, ok := commandControl(strings.ToLower(f[0]))
	return ok
}

// Keys lists every bound key.
func Keys() []string {
	var out []string
	for _, c := range Controls {
		out = append(out, c.Keys...)
	}
	return out
}

// State is a session's control values.
type State struct {
	Mode          string             `json:"mode"`
	Provider      string             `json:"provider,omitempty"`
	Model         string             `json:"model,omitempty"`
	Effort        string             `json:"effort,omitempty"`
	Guardrails    bool               `json:"guardrails"`
	Ask           bool               `json:"ask"`
	Caveman       bool               `json:"caveman"`
	AutoCommit    bool               `json:"autoCommit"`
	Reasoning     string             `json:"reasoning"`
	Tools         string             `json:"tools"`
	Decisions     string             `json:"decisions"`
	TestsPostEnd  string             `json:"testsPostEnd"`
	TestsOnFail   string             `json:"testsOnFail"`
	LSP           bool               `json:"lsp"`
	LSPLanguages  map[string]bool    `json:"lspLanguages,omitempty"`
	JevJobs       map[string]bool    `json:"jevJobs,omitempty"`
	JevThresholds map[string]float64 `json:"jevThresholds,omitempty"`
}

// FromSettings is the state a session starts with: the resolved settings, the
// mode it was started in and the model it resolved. Display filters start on
// auto, which defers to the settings.
func FromSettings(s config.Settings, mode, provider, model, effort string) State {
	if !slices.Contains(Modes, mode) {
		mode = "agent"
	}
	st := State{
		Mode: mode, Provider: provider, Model: model, Effort: effort,
		Guardrails:   s.GuardrailsEnabled(),
		Ask:          s.AskPermissionEnabled(),
		Caveman:      s.CavemanEnabled(),
		AutoCommit:   s.AutoCommitPerTaskEnabled(),
		Reasoning:    "auto",
		Tools:        "auto",
		Decisions:    s.InternalWorkLevel(),
		TestsPostEnd: s.TestsPostEnd(),
		TestsOnFail:  s.TestsOnFail(),
		LSP:          s.LSPEnabled(),
	}
	if s.LSP != nil && len(s.LSP.Languages) > 0 {
		st.LSPLanguages = maps.Clone(s.LSP.Languages)
	}
	if s.Jev != nil {
		if len(s.Jev.Jobs) > 0 {
			st.JevJobs = maps.Clone(s.Jev.Jobs)
		}
		if s.Jev.Thresholds != nil {
			for _, k := range config.JevThresholdKeys() {
				if v, set, _ := s.Jev.Thresholds.Value(k); set {
					if st.JevThresholds == nil {
						st.JevThresholds = map[string]float64{}
					}
					st.JevThresholds[k] = v
				}
			}
		}
	}
	return st
}

// Clone returns a deep copy.
func (st State) Clone() State {
	st.LSPLanguages = maps.Clone(st.LSPLanguages)
	st.JevJobs = maps.Clone(st.JevJobs)
	st.JevThresholds = maps.Clone(st.JevThresholds)
	return st
}

// Apply overlays the state on a copy of s. The model and mode are not settings
// keys and are left to the caller. The result is for this session only.
func (st State) Apply(s config.Settings) config.Settings {
	b := func(v bool) *bool { return &v }
	s.Guardrails = b(st.Guardrails)
	s.AskPermission = b(st.Ask)
	s.Caveman = b(st.Caveman)
	s.AutoCommitPerTask = b(st.AutoCommit)
	if st.Effort != "" {
		s.Effort = st.Effort
	}
	ui := config.UISettings{}
	if s.UI != nil {
		ui = *s.UI
	}
	switch st.Reasoning {
	case "shown":
		ui.ShowReasoning = b(true)
	case "hidden":
		ui.ShowReasoning = b(false)
	}
	switch st.Tools {
	case "all":
		ui.ShowToolCalls, ui.ShowEdits = b(true), b(true)
	case "edits":
		ui.ShowToolCalls, ui.ShowEdits = b(false), b(true)
	case "none":
		ui.ShowToolCalls, ui.ShowEdits = b(false), b(false)
	}
	if st.Decisions != "" {
		lvl := st.Decisions
		ui.ShowInternalWork = &lvl
	}
	s.UI = &ui
	tests := config.TestsSettings{}
	if s.Tests != nil {
		tests = *s.Tests
	}
	tests.PostEnd = st.TestsPostEnd
	tests.OnFail = st.TestsOnFail
	s.Tests = &tests
	lsp := config.LSPSettings{}
	if s.LSP != nil {
		lsp = *s.LSP
	}
	lsp.Enabled = b(st.LSP)
	lsp.Languages = maps.Clone(st.LSPLanguages)
	s.LSP = &lsp
	jev := config.JevSettings{}
	if s.Jev != nil {
		jev = *s.Jev
	}
	jev.Jobs = maps.Clone(st.JevJobs)
	jev.Thresholds = st.thresholds()
	s.Jev = &jev
	return s
}

func (st State) thresholds() *config.JevThresholdSettings {
	var t *config.JevThresholdSettings
	for _, k := range config.JevThresholdKeys() {
		if v, ok := st.JevThresholds[k]; ok {
			v := v
			t = t.With(k, &v)
		}
	}
	return t
}

// Env is what a parse needs from the session it applies to. Nil funcs refuse
// the controls that need them.
type Env struct {
	// AllowGuardrailsOff permits turning guardrails off. Without it a request
	// to switch them off is refused.
	AllowGuardrailsOff bool
	// CheckModel returns "" when the provider and model may be used here, or
	// the reason they may not.
	CheckModel func(provider, model, effort string) string
	// Efforts lists the reasoning levels of a provider and model.
	Efforts func(provider, model string) []string
	// Swap returns the model ctrl+q switches to from the current one, false
	// when no separate fast model is configured.
	Swap func(st State) (provider, model string, ok bool)
}

// Change is one applied control: the new state, the control it touched and a
// summary the harness composed.
type Change struct {
	Control string
	Summary string
	State   State
	// Rebuild is true when the agent session must be rebuilt.
	Rebuild bool
}

// ErrUnknown is returned for a line or key no control owns.
var ErrUnknown = errors.New("not a session control")

// Parse applies one slash line to st.
func Parse(line string, st State, env Env) (Change, error) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) == 0 {
		return Change{}, ErrUnknown
	}
	c, ok := commandControl(strings.ToLower(f[0]))
	if !ok {
		return Change{}, ErrUnknown
	}
	args := f[1:]
	for i := range args {
		if c.ID != CtlModel {
			args[i] = strings.ToLower(args[i])
		}
	}
	next := st.Clone()
	var summary string
	var err error
	switch c.ID {
	case CtlMode:
		summary, err = choice(args, Modes, &next.Mode, "mode")
	case CtlModel:
		summary, err = setModel(args, &next, env)
	case CtlEffort:
		summary, err = setEffort(args, &next, env)
	case CtlGuardrails:
		summary, err = toggle(args, &next.Guardrails, "guardrails")
		if err == nil && !next.Guardrails && !env.AllowGuardrailsOff {
			err = errors.New("this host does not let a web session turn guardrails off")
		}
	case CtlAsk:
		summary, err = toggle(args, &next.Ask, "ask")
	case CtlCaveman:
		summary, err = toggle(args, &next.Caveman, "caveman")
	case CtlAutoCommit:
		summary, err = toggle(args, &next.AutoCommit, "auto-commit")
	case CtlReasoning:
		summary, err = choice(args, ReasoningModes, &next.Reasoning, "reasoning display")
	case CtlTools:
		summary, err = choice(args, ToolModes, &next.Tools, "tool-call display")
	case CtlDecisions:
		summary, err = choice(args, DecisionLevels, &next.Decisions, "decisions display")
	case CtlTests:
		summary, err = setTests(args, &next)
	case CtlLSP:
		summary, err = setLSP(args, &next)
	case CtlJev:
		summary, err = setJev(args, &next)
	}
	if err != nil {
		return Change{}, err
	}
	return Change{Control: c.ID, Summary: summary, State: next, Rebuild: c.Rebuild}, nil
}

// ParseKey applies one key press to st, the way the TUI's key does.
func ParseKey(key string, st State, env Env) (Change, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	next := st.Clone()
	var line string
	switch key {
	case "shift+tab", "f5":
		line = "/mode " + cycle(CycleModes, st.Mode)
	case "f2":
		line = "/caveman " + flip(st.Caveman)
	case "f3":
		line = "/guardrails " + flip(st.Guardrails)
	case "f4":
		line = "/ask " + flip(st.Ask)
	case "f6":
		if env.Efforts == nil {
			return Change{}, errors.New("reasoning effort cannot be changed here")
		}
		levels := append([]string{"default"}, env.Efforts(st.Provider, st.Model)...)
		cur := st.Effort
		if cur == "" {
			cur = "default"
		}
		line = "/effort " + cycle(levels, cur)
	case "ctrl+r":
		line = "/reasoning " + cycle(ReasoningModes, st.Reasoning)
	case "ctrl+t":
		line = "/tools " + cycle(ToolModes, st.Tools)
	case "ctrl+q":
		if env.Swap == nil {
			return Change{}, errors.New("no separate fast model is configured, so there is nothing to switch to")
		}
		p, m, ok := env.Swap(next)
		if !ok {
			return Change{}, errors.New("no separate fast model is configured, so there is nothing to switch to")
		}
		line = "/model " + p + " " + m
	default:
		return Change{}, ErrUnknown
	}
	return Parse(line, st, env)
}

func cycle(vals []string, cur string) string {
	i := slices.Index(vals, cur)
	return vals[(i+1)%len(vals)]
}

func flip(on bool) string {
	if on {
		return "off"
	}
	return "on"
}

func onOffLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func toggle(args []string, dst *bool, label string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("%s takes on or off", label)
	}
	switch args[0] {
	case "on":
		*dst = true
	case "off":
		*dst = false
	default:
		return "", fmt.Errorf("%s takes on or off", label)
	}
	return label + ": " + args[0], nil
}

func choice(args []string, vals []string, dst *string, label string) (string, error) {
	if len(args) != 1 || !slices.Contains(vals, args[0]) {
		return "", fmt.Errorf("%s takes one of %s", label, strings.Join(vals, ", "))
	}
	*dst = args[0]
	return label + ": " + args[0], nil
}

func setModel(args []string, st *State, env Env) (string, error) {
	if env.CheckModel == nil {
		return "", errors.New("the model cannot be changed here")
	}
	if len(args) < 2 || len(args) > 3 {
		return "", errors.New("usage: /model <provider> <model> [effort]")
	}
	prov, model, effort := strings.ToLower(args[0]), args[1], ""
	if len(args) == 3 {
		effort = strings.ToLower(args[2])
	}
	if why := env.CheckModel(prov, model, effort); why != "" {
		return "", errors.New(why)
	}
	st.Provider, st.Model = prov, model
	if effort != "" {
		st.Effort = effort
	}
	return "model: " + prov + "/" + model, nil
}

func setEffort(args []string, st *State, env Env) (string, error) {
	if len(args) != 1 {
		return "", errors.New("usage: /effort default|<level>")
	}
	if args[0] == "default" {
		st.Effort = ""
		return "reasoning effort: default", nil
	}
	if env.Efforts == nil || !slices.Contains(env.Efforts(st.Provider, st.Model), args[0]) {
		return "", fmt.Errorf("%s does not take that reasoning effort", st.Provider)
	}
	st.Effort = args[0]
	return "reasoning effort: " + args[0], nil
}

func setTests(args []string, st *State) (string, error) {
	if len(args) != 2 {
		return "", errors.New("usage: /tests post_end off|goal|goal_plan|session, or /tests on_fail off|diagnose|fix")
	}
	switch args[0] {
	case "post_end":
		return choice(args[1:], TestsPostEnd, &st.TestsPostEnd, "tests after the turn")
	case "on_fail":
		return choice(args[1:], TestsOnFail, &st.TestsOnFail, "tests on failure")
	}
	return "", errors.New("tests takes post_end or on_fail")
}

func setLSP(args []string, st *State) (string, error) {
	switch len(args) {
	case 1:
		return toggle(args, &st.LSP, "language servers")
	case 2:
		if !slices.Contains(config.KnownLSPLanguages, args[1]) {
			return "", fmt.Errorf("unknown language; one of %s", strings.Join(config.KnownLSPLanguages, ", "))
		}
		var on bool
		if _, err := toggle(args[:1], &on, "language server"); err != nil {
			return "", err
		}
		if st.LSPLanguages == nil {
			st.LSPLanguages = map[string]bool{}
		}
		st.LSPLanguages[args[1]] = on
		return "language server " + args[1] + ": " + onOffLabel(on), nil
	}
	return "", errors.New("usage: /lsp on|off [language]")
}

func setJev(args []string, st *State) (string, error) {
	if len(args) == 0 {
		return "", errors.New("usage: /jev job <name> on|off, /jev threshold <key> <0..1>, or /jev reset")
	}
	switch args[0] {
	case "reset":
		if len(args) != 1 {
			return "", errors.New("usage: /jev reset")
		}
		st.JevJobs, st.JevThresholds = nil, nil
		return "jev: jobs and thresholds back to the settings", nil
	case "job":
		if len(args) != 3 || !config.ValidJevJob(args[1]) {
			return "", errors.New("usage: /jev job <name> on|off, with a known job name")
		}
		var on bool
		if _, err := toggle(args[2:], &on, "jev job"); err != nil {
			return "", err
		}
		if st.JevJobs == nil {
			st.JevJobs = map[string]bool{}
		}
		st.JevJobs[args[1]] = on
		return "jev job " + args[1] + ": " + onOffLabel(on), nil
	case "threshold":
		if len(args) != 3 || !slices.Contains(config.JevThresholdKeys(), args[1]) {
			return "", errors.New("usage: /jev threshold <key> <0..1>, with a known key")
		}
		v, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			return "", errors.New("a threshold is a number between 0 and 1")
		}
		v = float64(int(v*100+0.5)) / 100
		cand := maps.Clone(st.JevThresholds)
		if cand == nil {
			cand = map[string]float64{}
		}
		cand[args[1]] = v
		t := State{JevThresholds: cand}.thresholds()
		if err := t.Validate(); err != nil {
			return "", err
		}
		st.JevThresholds = cand
		return fmt.Sprintf("jev threshold %s: %.2f", args[1], v), nil
	}
	return "", errors.New("jev takes job, threshold or reset")
}
