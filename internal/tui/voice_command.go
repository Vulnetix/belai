package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// Spoken instructions. After the wake word and keyword steps of voiceGate, a
// short utterance is offered to Jev's voice_command job with the things the user
// could mean: their skills, saved prompts and processes, agent profiles and
// crews, the security review, plan mode and goal mode. Exactly one must rate at
// or above jev.thresholds.voice_at (0.95 by default). Anything else, including
// no backend, a failure or two close matches, leaves the speech as ordinary
// dictation, so the job can only ever narrow what voice does.
//
// Each target runs through the function its slash command uses, so that
// function's own gates hold: the plan-mode check on a process, fleet preflight
// and the worker cap, the agent picker's name check. Nothing here runs a shell
// line, and a prompt only fills the composer for the user to send.

const (
	// voiceCommandMaxWords bounds what is treated as an instruction. A
	// paragraph of dictation is never one, and is not sent to the backend.
	voiceCommandMaxWords = 24
	// voiceCommandTimeout bounds the match, so dictation is never held up long.
	voiceCommandTimeout = 4 * time.Second
	// voiceTargetMax bounds the targets offered in one match.
	voiceTargetMax = 48
)

// voiceTarget is one thing an instruction may start, with how to start it.
type voiceTarget struct {
	jev.VoiceTarget
	// say is what the acknowledgement tells the user.
	say string
	run func(a *App) tea.Cmd
}

// voiceCommandMsg carries a finished match back to Update.
type voiceCommandMsg struct {
	gen     int
	text    string
	targets []voiceTarget
	id      string
	score   float64
	model   string
	took    time.Duration
	ran     bool // the backend answered: a miss is then a "none", not an outage
}

// voiceJobs returns the Jev job runner for voice, built once per classifier
// choice. nil when no decision backend is configured.
func (a *App) voiceJobs() *jev.Jobs {
	key := a.cfg.Classifier.Provider + "/" + a.cfg.Classifier.Model
	if a.voice.jobs == nil || a.voice.jobsKey != key {
		a.voice.jobs = run.NewJevJobs(a.cfg, a.settings.JevJobSet)
		a.voice.jobsKey = key
	}
	return a.voice.jobs
}

// voiceMatchable reports whether an utterance may be offered to the job.
func (a *App) voiceMatchable(text string) bool {
	n := len(strings.Fields(text))
	return n > 0 && n <= voiceCommandMaxWords &&
		a.settings.Voice.VoiceCommandsEnabled() &&
		a.settings.JevJobSet(config.JevVoiceCommand) &&
		a.voiceJobs().Enabled(config.JevVoiceCommand)
}

// voiceMatchCmd scores the utterance against the targets in the background.
func (a *App) voiceMatchCmd(text string, targets []voiceTarget) tea.Cmd {
	jobs := a.voiceJobs()
	gen := a.voice.cmdGen
	offered := make([]jev.VoiceTarget, len(targets))
	for i, t := range targets {
		offered[i] = t.VoiceTarget
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout)
		defer cancel()
		start := time.Now()
		scores, _, err := jobs.RateVoice(ctx, text, offered)
		m := voiceCommandMsg{gen: gen, text: text, targets: targets, model: jobs.Identity(), took: time.Since(start), ran: err == nil}
		if err == nil {
			m.id, m.score = jev.PickVoice(scores)
		}
		return m
	}
}

// handleVoiceCommand applies a finished match: it runs the one target, or
// hands the speech back to dictation.
func (a *App) handleVoiceCommand(m voiceCommandMsg) tea.Cmd {
	v := &a.voice
	if m.gen != v.cmdGen || v.eng == nil {
		return nil // typing cancelled voice, or voice was turned off
	}
	var hit *voiceTarget
	for i := range m.targets {
		if m.targets[i].ID == m.id && m.id != "" {
			hit = &m.targets[i]
		}
	}
	if hit == nil {
		if m.ran {
			rolemanager.RecordVoiceCommand("none", "", 0, m.model, m.took)
		}
		return a.voiceDictate(m.text)
	}
	rolemanager.RecordVoiceCommand("matched", hit.Kind, int(m.score*100), m.model, m.took)
	a.voiceDropLive()
	a.voiceAck(hit.say)
	return hit.run(a)
}

// voiceDictate sends text down the ordinary dictation path, unless the composer
// can no longer take it (then it waits there as it always did).
func (a *App) voiceDictate(text string) tea.Cmd { return a.voiceEnqueue(text) }

// voiceTargets lists what a spoken instruction may start right now.
func (a *App) voiceTargets() []voiceTarget {
	a.loadSlashLib()
	var out []voiceTarget
	add := func(kind, name, desc, say string, run func(*App) tea.Cmd) {
		if len(out) >= voiceTargetMax || name == "" || sanitize.Ident(name, 64) != name {
			return // only plain identifiers are offered or named
		}
		id := "t" + string(rune('a'+len(out)/26)) + string(rune('a'+len(out)%26))
		out = append(out, voiceTarget{VoiceTarget: jev.VoiceTarget{ID: id, Kind: kind, Name: name, Description: desc}, say: say, run: run})
	}
	add("mode", "plan", "switch to plan mode, where the assistant writes a plan and changes nothing", "plan mode",
		func(a *App) tea.Cmd { return a.setOperatingMode("plan") })
	add("mode", "goal", "switch to goal mode, where the assistant works until the goal is met", "goal mode",
		func(a *App) tea.Cmd { return a.setOperatingMode("goal") })
	add("review", "security-review", "run the Vulnetix security review scanners on this project", "security review",
		func(a *App) tea.Cmd { return a.startReview() })
	for _, c := range agentprofile.ListCrews() {
		name := c.Name
		add("crew", name, "", "crew "+name, func(a *App) tea.Cmd {
			a.startFleetWorkers("", name, 0)
			return tea.Batch(a.openAgentsTab(agentTabFleet), a.fleetTick())
		})
	}
	for _, c := range a.agents {
		name := c.Name
		add("agent", name, "", "agent "+name, func(a *App) tea.Cmd { return a.engageAgent(name) })
	}
	for _, p := range a.slashLib.processes {
		name := p.Name
		add("process", name, "", "process "+name, func(a *App) tea.Cmd { return a.runProcessEntry(name) })
	}
	for _, p := range a.slashLib.prompts {
		name := p.Name
		add("prompt", name, "", "prompt "+name, func(a *App) tea.Cmd { return a.runPromptEntry(name) })
	}
	for _, s := range tools.InstalledSkills() {
		name := s.Name
		add("skill", name, "", "skill "+name, func(a *App) tea.Cmd { return a.voiceRunSkill(name) })
	}
	return out
}

// voiceRunSkill asks the model to use a skill, through the same Enter path as a
// typed prompt. A draft already in the composer is put back afterwards.
func (a *App) voiceRunSkill(name string) tea.Cmd {
	draft := a.editor.Value()
	a.editor.SetValue("use the " + name + " skill")
	cmd := a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if draft != "" && a.editor.Value() == "" {
		a.editor.SetValue(draft)
		a.editor.CursorEnd()
		a.relayout()
	}
	return cmd
}

// setOperatingMode is the mode switch /mode performs: sticky, saved, and
// re-resolved on the next send.
func (a *App) setOperatingMode(mode string) tea.Cmd {
	a.mode = mode
	a.modeExplicit = true
	a.modeSticky = true
	a.syncPlanMode()
	a.saveMode()
	a.persistCarrierMeta()
	a.addSystem("mode: " + mode)
	return nil
}
