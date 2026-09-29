package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/decisionserver"
	"github.com/vulnetix/belai/internal/localinfer"
	"github.com/vulnetix/belai/internal/models"
	"github.com/vulnetix/belai/internal/modeltest"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
)

// A /model edit that selects a model is staged, tested and written only when
// the test passes. Knobs that pick no model (thresholds, effort, reasoning,
// chunk, clears) keep writing at once.

// modelTester runs a test plan. It is a field so tests can substitute a fake.
type modelTester func(ctx context.Context, steps []modeltest.Step, env *modeltest.Env, emit func(modeltest.Event)) modeltest.Report

// stagedChange is one model selection waiting on its test.
type stagedChange struct {
	role   modelRole
	rowKey string
	// label names the change for the result line, e.g.
	// "classifier.model = decider-4b".
	label string
	// was names what stays in effect when the change is not saved.
	was string
	// write persists the change (the pre-existing write path).
	write func() tea.Cmd
	// scope names where write saves, for the result line.
	scope string
	steps []modeltest.Step
	// decisionModel is the local decision model under test, so a
	// re-download hint knows which file to delete.
	decisionModel *decisions.LocalModel
	// jevProfile names the self-hosted Jev profile whose address a test
	// correction updates on save.
	jevProfile string
	// restage rebuilds the change for a retry (fresh closures, same edit).
	restage func() tea.Cmd
	// chatKeys identify the chat probes in steps, remembered on a save.
	chatKeys []string
}

// modelTestStepView is one step as the panel shows it.
type modelTestStepView struct {
	name    string
	status  modeltest.Status
	detail  string
	started time.Time
	elapsed time.Duration
}

// modelTestRun is the running or finished test on the /model page.
type modelTestRun struct {
	gen     int
	change  stagedChange
	steps   []modelTestStepView
	started time.Time
	// confirm is a download offer awaiting y/n; reply receives the answer.
	confirm *modeltest.DownloadOffer
	reply   chan bool
	dlDone  int64
	dlTotal int64
	dlStart time.Time
	logLine string
	// outcome: running | saved | not-saved | cancelled | write-failed
	outcome string
	note    string
	hints   []modeltest.Hint
	cancel  context.CancelFunc
	events  chan tea.Msg
}

type modelTestEventMsg struct {
	gen int
	ev  modeltest.Event
}

type modelTestDoneMsg struct {
	gen int
	rep modeltest.Report
}

type modelTestTickMsg struct{ gen int }

// modelTestCacheTTL is how long a passed chat probe of the same model
// counts, so editing one routing candidate does not re-probe the pool.
const modelTestCacheTTL = 10 * time.Minute

func cloneSettings(s config.Settings) config.Settings {
	var out config.Settings
	b, err := json.Marshal(s)
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (a *App) credSource() run.CredentialSource {
	if a.resolver != nil {
		return a.resolver
	}
	return run.EnvSource(os.Getenv)
}

// localKind names the local server template a provider runs on.
func (a *App) localKind(name string) string {
	switch name {
	case "llama-server", "ollama":
		return name
	}
	if p, ok := a.settings.Providers[name]; ok {
		switch p.Kind {
		case "llama-server", "ollama":
			return p.Kind
		}
	}
	return ""
}

// chatTarget prepares one chat model for the test, on the UI goroutine.
func (a *App) chatTarget(role, providerName, model, effort string, sentinel bool) modeltest.ChatTarget {
	cfg, status := run.Prepare(model, providerName, a.credSource())
	if effort != "" {
		cfg.Effort = effort
	}
	t := modeltest.ChatTarget{
		Role: role, Cfg: cfg, Configured: status.Configured, Missing: status.Missing,
		Kind: a.localKind(cfg.Provider), Sentinel: sentinel,
	}
	if t.Kind == "llama-server" {
		if port := portOf(cfg.BaseURL); port > 0 {
			t.LaunchPort = port
		}
	}
	return t
}

func portOf(base string) int {
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	i := strings.LastIndexByte(base, ':')
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(base[i+1:])
	return n
}

func chatKey(t modeltest.ChatTarget) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%v", t.Cfg.Provider, t.Cfg.Model, t.Cfg.BaseURL, t.Sentinel)
}

// freshChats drops chat targets that passed a probe recently.
func (a *App) freshChats(in []modeltest.ChatTarget) []modeltest.ChatTarget {
	var out []modeltest.ChatTarget
	for _, t := range in {
		if at, ok := a.modelTestPassed[chatKey(t)]; ok && time.Since(at) < modelTestCacheTTL {
			continue
		}
		out = append(out, t)
	}
	return out
}

// classifierTarget builds the test for a candidate classifier block.
func (a *App) classifierTarget(cand *config.ClassifierSettings) (modeltest.Target, *decisions.LocalModel, string, error) {
	src := a.credSource()
	cc, err := run.ResolveClassifier(a.cfg, cand, src)
	if err != nil {
		return modeltest.Target{}, nil, "", err
	}
	sc := run.ResolveSecurityClassifier(cand)
	var t modeltest.Target
	var local *decisions.LocalModel
	jevProfile := ""
	cfg := a.cfg
	cfg.Classifier = cc
	cfg.Security = sc
	guard := run.GuardConfig(cfg)
	needGuard := sc.Kind != "models" || sc.Phase3On
	switch {
	case cc.Decisions.On():
		d := cc.Decisions
		if d.Key != nil {
			// The resolver is not safe off the UI goroutine: resolve the key
			// now and hand the probe the value.
			k, _ := d.Key()
			d.Key = func() (string, error) { return k, nil }
		}
		t.Decisions = &d
		if d.Backend == decisions.BackendLocal {
			m := d.Local
			local = &m
		} else {
			jevProfile = d.Provider
		}
		// The decision backend hands undecided checks to the agent model,
		// which must answer the sentinel.
		t.Chats = append(t.Chats, a.chatTarget("fallback", guard.Provider, guard.Model, "none", true))
	case jev.IsDecisionsModel(guard.Provider, guard.Model):
		key := guard.APIKey
		t.OpenRouterJev = jev.OpenRouterDecider(func() (string, error) { return key, nil })
		t.Chats = append(t.Chats, a.chatTarget("fallback", a.cfg.Provider, a.cfg.Model, "none", true))
	case needGuard:
		ct := a.chatTarget("classifier", guard.Provider, guard.Model, "none", true)
		ct.Cfg = guard
		ct.Cfg.Effort = cc.Effort
		t.Chats = append(t.Chats, ct)
	}
	if sc.Kind == "models" {
		t.Phases = &sc
	}
	t.Chats = a.freshChats(t.Chats)
	return t, local, jevProfile, nil
}

// stageClassifier tests a classifier edit and writes it on a pass.
func (a *App) stageClassifier(rowKey, label string, fn func(*config.ClassifierSettings)) tea.Cmd {
	cand := cloneSettings(a.settings)
	if cand.Classifier == nil {
		cand.Classifier = &config.ClassifierSettings{}
	}
	fn(cand.Classifier)
	was := "the current classifier"
	if cls := a.settings.Classifier; cls != nil && cls.Provider != "" {
		was = cls.Provider + " · " + orDash(cls.Model)
	}
	restage := func() tea.Cmd { return a.stageClassifier(rowKey, label, fn) }
	target, local, jevProfile, err := a.classifierTarget(cand.Classifier)
	ch := stagedChange{
		role: roleClassifier, rowKey: rowKey, label: label, was: was,
		write:   func() tea.Cmd { return a.mutateClassifier(fn) },
		scope:   a.modelState.classifierScope,
		restage: restage, decisionModel: local, jevProfile: jevProfile,
	}
	if err != nil {
		return a.failStaged(ch, "resolve", err.Error())
	}
	ch.plan(target)
	return a.startModelTest(ch)
}

// stageRouting tests a routing edit (a use case, the fast tier, the kind)
// and writes it on a pass.
func (a *App) stageRouting(role modelRole, rowKey, label string, fn func(*config.RoutingSettings)) tea.Cmd {
	cand := cloneSettings(a.settings)
	if cand.Routing == nil {
		cand.Routing = &config.RoutingSettings{}
	}
	if cand.Routing.UseCases == nil {
		cand.Routing.UseCases = map[string]config.RoutingTarget{}
	}
	fn(cand.Routing)
	restage := func() tea.Cmd { return a.stageRouting(role, rowKey, label, fn) }
	ch := stagedChange{
		role: role, rowKey: rowKey, label: label, was: "the current routing",
		write:   func() tea.Cmd { return a.mutateRouting(fn) },
		scope:   a.modelState.routingScope,
		restage: restage,
	}
	rc, err := run.ResolveRouting(a.cfg, cand.Routing, a.credSource())
	if err != nil {
		return a.failStaged(ch, "resolve", err.Error())
	}
	var t modeltest.Target
	noJev := false
	if role == roleFast {
		if rc.Fast != nil {
			t.Chats = append(t.Chats, a.chatTarget("fast", rc.Fast.Provider, rc.Fast.Model, "none", false))
		}
	} else if rc.Kind == config.RoutingRouted {
		for _, c := range rc.Candidates {
			if strings.HasPrefix(rowKey, "route:") && "route:"+c.Key != rowKey {
				continue
			}
			t.Chats = append(t.Chats, a.chatTarget("route "+c.Key, c.Cfg.Provider, c.Cfg.Model, "", false))
		}
		if rowKey == "kind" {
			cfg := a.cfg
			cfg.Routing = rc
			if !cfg.ClassifierOrDefault().Decisions.On() {
				key := ""
				if rc.JevToken != nil {
					key, _ = rc.JevToken()
				}
				if key != "" {
					t.OpenRouterJev = jev.OpenRouterDecider(func() (string, error) { return key, nil })
				} else {
					noJev = true
				}
			}
		}
	}
	t.Chats = a.freshChats(t.Chats)
	ch.plan(t)
	if noJev {
		// Routed mode without a Jev backend is valid (every use case falls
		// back to the work model) but pointless: save it, and say so.
		ch.steps = append(ch.steps, modeltest.Step{Name: "router", Run: func(context.Context, *modeltest.State) modeltest.Outcome {
			return modeltest.Outcome{Status: modeltest.StatusWarn,
				Detail: "no Jev backend: every use case uses the work model until one is set",
				Hints:  []modeltest.Hint{{Key: "p", Text: "set an OpenRouter key, or pick a local or self-hosted decision model as the classifier", Action: modeltest.ActProviders}}}
		}})
	}
	return a.startModelTest(ch)
}

// stageAgent tests a new agent provider/model and writes it on a pass.
func (a *App) stageAgent(rowKey, providerName, model string, fn func(*config.Settings), sessionFn func()) tea.Cmd {
	restage := func() tea.Cmd { return a.stageAgent(rowKey, providerName, model, fn, sessionFn) }
	scope := a.modelState.agentScope
	ch := stagedChange{
		role: roleAgent, rowKey: rowKey,
		label:   "agent = " + providerName + " · " + orDash(model),
		was:     a.cfg.Provider + " · " + orDash(a.cfg.Model),
		write:   func() tea.Cmd { return a.mutateAgent(fn, sessionFn) },
		scope:   scope,
		restage: restage,
	}
	t := modeltest.Target{Chats: a.freshChats([]modeltest.ChatTarget{a.chatTarget("agent", providerName, model, a.settings.Effort, false)})}
	ch.plan(t)
	return a.startModelTest(ch)
}

func orDash(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

// failStaged shows a change that could not even be tested.
func (a *App) failStaged(ch stagedChange, step, detail string) tea.Cmd {
	if a.modelTest != nil && a.modelTest.cancel != nil {
		a.modelTest.cancel()
	}
	a.modelTestGen++
	a.modelTest = &modelTestRun{
		gen: a.modelTestGen, change: ch, started: time.Now(), outcome: "not-saved",
		steps: []modelTestStepView{{name: step, status: modeltest.StatusFail, detail: detail}},
		hints: []modeltest.Hint{{Key: "p", Text: "check the provider in providers", Action: modeltest.ActProviders}},
	}
	return nil
}

// startModelTest runs a staged change's steps in the background.
func (a *App) startModelTest(ch stagedChange) tea.Cmd {
	if a.modelTest != nil && a.modelTest.cancel != nil {
		a.modelTest.cancel()
	}
	a.modelTestGen++
	gen := a.modelTestGen
	ctx, cancel := context.WithCancel(context.Background())
	run := &modelTestRun{
		gen: gen, change: ch, started: time.Now(), outcome: "running",
		cancel: cancel, events: make(chan tea.Msg, 64), reply: make(chan bool, 1),
	}
	for _, s := range ch.steps {
		run.steps = append(run.steps, modelTestStepView{name: s.Name})
	}
	a.modelTest = run
	a.modelState.errorMsg = ""
	if len(ch.steps) == 0 {
		// Nothing selects a model that needs checking (for example the
		// classifier left to inherit the already-tested agent model).
		return a.handleModelTestDone(modelTestDoneMsg{gen: gen, rep: modeltest.Report{Passed: true}})
	}
	env := &modeltest.Env{
		Client:   a.client,
		HFToken:  a.hfToken(),
		Registry: a.activity,
		NGL:      -1,
		Confirm: func(ctx context.Context, o modeltest.DownloadOffer) bool {
			select {
			case ok := <-run.reply:
				return ok
			case <-ctx.Done():
				return false
			}
		},
	}
	if a.modelTestCPU {
		env.NGL = 0
	}
	tester := a.modelTester
	if tester == nil {
		tester = testModelTester
	}
	if tester == nil {
		tester = modeltest.Run
	}
	steps := ch.steps
	if testModelTestSync && !a.modelTestAsync {
		// Tests: run inline so a key press settles before it returns.
		rep := tester(ctx, steps, env, func(ev modeltest.Event) { a.applyModelTestEvent(run, ev) })
		return a.handleModelTestDone(modelTestDoneMsg{gen: gen, rep: rep})
	}
	go func() {
		rep := tester(ctx, steps, env, func(ev modeltest.Event) {
			// Progress events are frequent; drop one rather than block the
			// probe when the UI is behind.
			if ev.Kind == modeltest.EventProgress || ev.Kind == modeltest.EventLog {
				select {
				case run.events <- modelTestEventMsg{gen: gen, ev: ev}:
				default:
				}
				return
			}
			run.events <- modelTestEventMsg{gen: gen, ev: ev}
		})
		run.events <- modelTestDoneMsg{gen: gen, rep: rep}
	}()
	return tea.Batch(a.watchModelTest(run), a.modelTestTick(gen))
}

func (a *App) watchModelTest(run *modelTestRun) tea.Cmd {
	ch := run.events
	return func() tea.Msg { return <-ch }
}

func (a *App) modelTestTick(gen int) tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return modelTestTickMsg{gen: gen} })
}

func (a *App) handleModelTestTick(m modelTestTickMsg) tea.Cmd {
	if a.modelTest == nil || a.modelTest.gen != m.gen || a.modelTest.outcome != "running" {
		return nil
	}
	return a.modelTestTick(m.gen)
}

func (a *App) handleModelTestEvent(m modelTestEventMsg) tea.Cmd {
	run := a.modelTest
	if run == nil || run.gen != m.gen {
		return nil
	}
	a.applyModelTestEvent(run, m.ev)
	return a.watchModelTest(run)
}

// applyModelTestEvent folds one event into the panel state.
func (a *App) applyModelTestEvent(run *modelTestRun, ev modeltest.Event) {
	if ev.Index >= 0 && ev.Index < len(run.steps) {
		s := &run.steps[ev.Index]
		switch ev.Kind {
		case modeltest.EventStart:
			s.status, s.started = modeltest.StatusRunning, time.Now()
			run.dlDone, run.dlTotal, run.logLine = 0, 0, ""
		case modeltest.EventDone:
			s.status, s.detail, s.elapsed = ev.Outcome.Status, ev.Outcome.Detail, ev.Elapsed
			if ev.Outcome.Status == modeltest.StatusFail {
				run.hints = ev.Outcome.Hints
			} else if ev.Outcome.Status == modeltest.StatusWarn {
				run.hints = append(run.hints, ev.Outcome.Hints...)
			}
			run.confirm = nil
		case modeltest.EventProgress:
			if run.dlStart.IsZero() {
				run.dlStart = time.Now()
			}
			run.dlDone, run.dlTotal = ev.Done, ev.Total
		case modeltest.EventConfirm:
			run.confirm = ev.Offer
		case modeltest.EventLog:
			run.logLine = ev.Line
		}
	}
}

func (a *App) handleModelTestDone(m modelTestDoneMsg) tea.Cmd {
	run := a.modelTest
	if run == nil || run.gen != m.gen {
		// A superseded run: release anything it launched.
		m.rep.Release()
		return nil
	}
	run.confirm = nil
	if run.outcome == "cancelled" {
		m.rep.Release()
		return nil
	}
	rep := m.rep
	if !rep.Passed {
		run.outcome = "not-saved"
		rep.Release()
		if f, ok := rep.Failure(); ok {
			run.hints = f.Outcome.Hints
		}
		return nil
	}
	if run.change.jevProfile != "" && rep.Fix != nil {
		if err := a.saveJevFix(run.change.jevProfile, rep.Fix); err != nil {
			run.note = "the working address could not be saved: " + err.Error()
		} else {
			run.note = "saved the address that answered: " + rep.Fix.BaseURL + rep.Fix.Path
		}
	}
	cmd := run.change.write()
	if a.modelState.errorMsg != "" {
		run.outcome = "write-failed"
		run.note = a.modelState.errorMsg
		a.modelState.errorMsg = ""
		rep.Release()
		return cmd
	}
	run.outcome = "saved"
	a.adoptTestServers(run.change, rep)
	a.rememberPassedChats(run.change)
	return tea.Batch(cmd, a.warmDecisionsNote())
}

// rememberPassedChats records the chat targets of a saved change.
func (a *App) rememberPassedChats(ch stagedChange) {
	if a.modelTestPassed == nil {
		a.modelTestPassed = map[string]time.Time{}
	}
	for _, k := range ch.chatKeys {
		a.modelTestPassed[k] = time.Now()
	}
}

// plan sets a staged change's steps from its target.
func (ch *stagedChange) plan(t modeltest.Target) {
	ch.steps = modeltest.Plan(t)
	ch.chatKeys = nil
	for _, c := range t.Chats {
		ch.chatKeys = append(ch.chatKeys, chatKey(c))
	}
}

// adoptTestServers keeps what a passed test launched: a decision server
// joins this process's supervisor (stopped on exit), and a chat llama-server
// gets its port recorded as the llama-server credential.
func (a *App) adoptTestServers(ch stagedChange, rep modeltest.Report) {
	if rep.Handle != nil && ch.decisionModel != nil {
		decisionserver.Shared(*ch.decisionModel).Adopt(rep.Handle)
	}
	if rep.ChatServer != nil {
		_ = a.persistLocalServerCredentials(rep.ChatServer.Port)
	}
}

// saveJevFix records the address a self-hosted Jev test found answering, in
// the settings layer that defines the profile.
func (a *App) saveJevFix(name string, fix *modeltest.EndpointFix) error {
	for _, scope := range []config.Scope{config.ScopeGlobal, config.ScopeProject} {
		doc, err := config.OpenSettings(scope, a.workdir)
		if err != nil || doc == nil {
			continue
		}
		if _, ok := doc.Settings.Providers[name]; !ok {
			continue
		}
		return config.Mutate(scope, a.workdir, func(s *config.Settings) error {
			p := s.Providers[name]
			p.BaseURL = fix.BaseURL
			if fix.Path == decisions.DefaultSystemOnePath {
				p.DecisionPath = ""
			} else {
				p.DecisionPath = fix.Path
			}
			s.Providers[name] = p
			return nil
		})
	}
	return fmt.Errorf("provider %s is not in a settings file belai can edit", name)
}

// warmDecisionsNote starts the local decision server for a saved selection
// and surfaces a notice when it cannot start.
func (a *App) warmDecisionsNote() tea.Cmd {
	if n := run.WarmDecisions(a.cfg); n != "" {
		a.avail.note = n
	}
	return nil
}

// cancelModelTest stops a running test; nothing is written.
func (a *App) cancelModelTest() {
	run := a.modelTest
	if run == nil || run.outcome != "running" {
		return
	}
	run.outcome = "cancelled"
	run.confirm = nil
	if run.cancel != nil {
		run.cancel()
	}
}

// answerModelTestConfirm answers a pending download offer.
func (a *App) answerModelTestConfirm(yes bool) {
	run := a.modelTest
	if run == nil || run.confirm == nil {
		return
	}
	run.confirm = nil
	select {
	case run.reply <- yes:
	default:
	}
}

// applyModelTestHint runs a hint's action.
func (a *App) applyModelTestHint(act modeltest.Action) tea.Cmd {
	run := a.modelTest
	if run == nil || run.change.restage == nil {
		return nil
	}
	switch act {
	case modeltest.ActRetry:
		return run.change.restage()
	case modeltest.ActCPU:
		a.modelTestCPU = true
		return run.change.restage()
	case modeltest.ActRedownload:
		if m := run.change.decisionModel; m != nil {
			decisionserver.Shared(*m).Stop()
			if err := localinfer.RemoveModelFile(m.Repo, m.File); err != nil {
				run.note = "could not delete the model file: " + err.Error()
				return nil
			}
		}
		return run.change.restage()
	case modeltest.ActProviders:
		return a.push(viewProviders)
	}
	return nil
}

// modelTestKey handles keys while the test panel is up. It reports whether
// the key was consumed.
func (a *App) modelTestKey(key string) (tea.Cmd, bool) {
	run := a.modelTest
	if run == nil {
		return nil, false
	}
	if run.confirm != nil {
		switch key {
		case "y", "Y", "enter":
			a.answerModelTestConfirm(true)
			return nil, true
		case "n", "N", "esc":
			a.answerModelTestConfirm(false)
			return nil, true
		}
		return nil, true
	}
	if run.outcome == "running" {
		if key == "esc" {
			a.cancelModelTest()
			return nil, true
		}
		return nil, false
	}
	for _, h := range run.hints {
		if h.Key != "" && h.Key == key {
			return a.applyModelTestHint(h.Action), true
		}
	}
	if key == "r" && run.outcome != "saved" {
		return a.applyModelTestHint(modeltest.ActRetry), true
	}
	return nil, false
}

// testingSuffix is the row marker while a change to that row is under test.
func (a *App) testingSuffix(role modelRole, key string) string {
	run := a.modelTest
	if run == nil || run.outcome != "running" || run.change.role != role || run.change.rowKey != key {
		return ""
	}
	n := 0
	for _, s := range run.steps {
		if s.status != "" && s.status != modeltest.StatusRunning {
			n++
		}
	}
	return fmt.Sprintf("  testing… %d/%d", min(n+1, len(run.steps)), len(run.steps))
}

// providerIsDecisions reports whether a provider name is a decision backend
// (never offered for chat roles).
func (a *App) providerIsDecisions(name string) bool {
	if name == decisions.LocalProvider {
		return true
	}
	p, ok := a.settings.Providers[name]
	return ok && p.Kind == decisions.JevKind
}

// decisionCatalog is the model list for a decision provider: the catalogue of
// local decision models, each marked with its size and whether it is on
// disk, or a self-hosted Jev profile's own model list.
func (a *App) decisionCatalog(name string) []models.Model {
	if name == decisions.LocalProvider {
		out := make([]models.Model, 0, len(decisions.LocalModels))
		for _, m := range decisions.LocalModels {
			where := "download " + modeltest.Sizes(m.SizeBytes) + " on select"
			if localinfer.FindModelFile(m.Repo, m.File) != "" {
				where = "on disk ✓"
			}
			out = append(out, models.Model{ID: m.ID, Label: m.Label + " · " + where + " · " + m.Blurb})
		}
		return out
	}
	var out []models.Model
	if p, ok := a.settings.Providers[name]; ok {
		for _, m := range p.Models {
			out = append(out, models.Model{ID: m.ID, Label: "self-hosted Jev"})
		}
	}
	if len(out) == 0 {
		out = append(out, models.Model{ID: "jev", Label: "self-hosted Jev · the server's default model"})
	}
	return out
}

// testModelTester and testModelTestSync are set by the package's tests: a
// default tester, run inline. Production leaves them zero.
var (
	testModelTester   modelTester
	testModelTestSync bool
)
