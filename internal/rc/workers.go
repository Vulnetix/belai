package rc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// workerName is a profile or crew name as a start request may carry it. It
// must start with a letter or digit, so it can never read as a flag.
var workerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)

// maxStartReport caps the report and refusal text sent back to the website.
const maxStartReport = 400

// startWorkers validates a worker or crew request against this host's own
// directory list and catalogue, then starts it. It returns the start report,
// or the reason it refused.
func (d *Daemon) startWorkers(r sessionsync.Dispatch) (string, string) {
	return d.startWorkersDrain(r, false)
}

// startWorkersDrain is startWorkers for a stored schedule, which sets drain:
// the worker exits once nothing is left to claim, whatever cron schedule its
// profile carries, because the stored schedule is what starts it.
func (d *Daemon) startWorkersDrain(r sessionsync.Dispatch, drain bool) (string, string) {
	cwd, ok := Allowed(d.o.Dirs, r.Cwd)
	if !ok {
		return "", "this host does not offer that directory"
	}
	// The request's model is checked like a session's: a provider this host
	// holds credentials for, and values that cannot read as flags.
	if why := checkOverride(r.Provider, r.Model, "", d.o.Models); why != "" {
		return "", why
	}
	w := WorkerStart{Exe: d.o.Exe, Cwd: cwd, MaxWorkers: d.o.MaxWorkers, Drain: drain, Provider: r.Provider, Model: r.Model,
		Controls: d.o.Controls, GuardrailsOff: d.o.Controls && d.o.GuardrailsOff, Trigger: encodeTrigger(r.Trigger)}
	// A worker cannot ask: a call that would ask blocks its item. A request's
	// ask: false (the organization's ask_permission for factory runs) makes
	// those calls allowed instead; deny rules, the posture, guardrails and the
	// sandbox still apply. ask: true and no ask keep the block.
	w.AskOff = r.Ask != nil && !*r.Ask
	// A stored schedule drains by design, so it never stays. Otherwise the
	// request's idle cap is clamped here, so no value reaches the argv above
	// the fleet's hard maximum or below zero.
	if r.Stay && !drain {
		w.Stay = true
		if r.StayIdleMax > 0 {
			w.StayIdleMax = fleet.StayIdleCap(time.Duration(r.StayIdleMax) * time.Second)
		}
	}
	inv := d.o.Inventory()
	switch r.Kind {
	case "worker":
		if !workerName.MatchString(r.Profile) {
			return "", "that is not a profile name"
		}
		if !slices.ContainsFunc(inv.Profiles, func(p sessionsync.RCProfile) bool { return p.Name == r.Profile }) {
			return "", "this host has no worker profile " + r.Profile
		}
		w.Profile = r.Profile
	case "crew":
		if !workerName.MatchString(r.Crew) {
			return "", "that is not a crew name"
		}
		if !slices.ContainsFunc(inv.Crews, func(c sessionsync.RCCrew) bool { return c.Name == r.Crew }) {
			return "", "this host has no crew " + r.Crew
		}
		w.Crew, w.Fill = r.Crew, r.Fill
	}
	report, err := d.o.StartWorkers(w)
	if err != nil {
		return "", clip(err.Error())
	}
	return clip(report), ""
}

// checkAgentProfile validates the agent profile a start request names: it needs
// agent mode, and the name must be one this host offers (SessionAgents), so a
// name from the website cannot reach a profile this host never advertised.
// An empty name is the default agent. In the pix-assistant mode a persona is
// required and it must be one of PixPersonas instead: they are worker
// profiles, so SessionAgents never lists them, and no other profile is taken.
func (d *Daemon) checkAgentProfile(name, mode string) string {
	if IsPixMode(mode) {
		if _, ok := PixPersonaName(name); !ok {
			return pixPersonaRefusal
		}
		return ""
	}
	if name == "" {
		return ""
	}
	if mode != "agent" {
		return "an agent profile needs agent mode"
	}
	if !ValidAgentName(name) {
		return "that is not an agent profile name"
	}
	if !slices.ContainsFunc(d.o.Inventory().Agents, func(a sessionsync.RCAgent) bool { return a.Name == name }) {
		return "this host has no agent profile " + name
	}
	return ""
}

// runAgentStart runs `belai agent start` in the directory, which does the
// trust check, the preflight and the agents.max_workers check (raised or
// lowered by belai rc --max), and waits briefly for the workers to register.
func runAgentStart(w WorkerStart) (string, error) {
	args := agentStartArgs(w)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Exe, args...)
	cmd.Dir = w.Cwd
	// The workers, and a crew run's supervisor and the members it starts
	// later, inherit this environment, so the trigger reaches each of them.
	cmd.Env = TriggerEnv(os.Environ(), w.Trigger)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return "", startError(text)
	}
	return text, nil
}

// agentStartArgs is the fixed argv of one `belai agent start`: the only words
// a request contributes are a profile or crew name, a provider and a model, each
// already checked and none able to start with a dash.
func agentStartArgs(w WorkerStart) []string {
	args := []string{"agent", "start"}
	if w.MaxWorkers > 0 {
		args = append(args, "-max-workers", strconv.Itoa(w.MaxWorkers))
	}
	if w.Drain {
		args = append(args, "-drain")
	} else if w.Stay {
		args = append(args, "-stay")
		if w.StayIdleMax > 0 {
			args = append(args, "-stay-idle-max", fleet.StayIdleCap(w.StayIdleMax).String())
		}
	}
	if w.Provider != "" {
		args = append(args, "-provider", w.Provider)
	}
	if w.Model != "" {
		args = append(args, "-model", w.Model)
	}
	if w.AskOff {
		args = append(args, "-ask-off")
	}
	if w.Controls {
		args = append(args, "-web-controls")
		if w.GuardrailsOff {
			args = append(args, "-web-allow-guardrails-off")
		}
	}
	if w.Crew != "" {
		args = append(args, "-crew", w.Crew)
		if w.Fill {
			args = append(args, "-fill")
		}
	} else {
		args = append(args, w.Profile)
	}
	return args
}

type startError string

func (e startError) Error() string { return string(e) }

// goDumpMarkers begin the part of a crashed Go program's output that is only
// stack frames. The cause (a thread that could not be made, a panic message) is
// above them.
var goDumpMarkers = []string{"\nruntime stack:", "\ngoroutine "}

// clip keeps the last line-joined maxStartReport bytes: the end of the
// command's output carries the result or the error. A Go crash dump is the
// exception: its end is stack frames, so it is cut where the frames begin and
// the cause above them is kept.
func clip(s string) string {
	for _, m := range goDumpMarkers {
		if i := strings.Index(s, m); i > 0 {
			s = s[:i]
		}
	}
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " · ")), " ")
	if len(s) > maxStartReport {
		s = "…" + strings.ToValidUTF8(s[len(s)-maxStartReport:], "")
	}
	return s
}

// setWorkerPaused asks one of this host's live workers to pause or resume. It
// reads the worker from the registry, so an id the host does not run, or a
// worker that already ended, is refused.
func setWorkerPaused(id string, pause bool) error {
	reg, err := fleet.OpenRegistry(nil)
	if err != nil {
		return err
	}
	rec, err := reg.Get(id)
	if err != nil || !rec.State.Live() {
		return errors.New("that worker is not running on this host")
	}
	return reg.SetPaused(id, pause)
}

// runKnowledgeIndex indexes a profile's documents by running
// `belai agent knowledge -index -json NAME` in dir, a trusted directory this
// daemon offers. That command does the trust check and runs each chunk through
// the security classifier, so what an install indexes is held to the same rules as
// what the agent indexes itself. The clause it returns carries a count only.
func runKnowledgeIndex(ctx context.Context, exe, dir, profile string) (string, error) {
	cmd := exec.CommandContext(ctx, exe, "agent", "knowledge", "-index", "-json", profile)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(errOut.String())
		if text == "" {
			text = err.Error()
		}
		return "", startError(clip(text))
	}
	var rep struct {
		Indexes []struct {
			Name      string            `json:"name"`
			Documents []json.RawMessage `json:"documents"`
		} `json:"indexes"`
	}
	if json.Unmarshal(out.Bytes(), &rep) == nil {
		for _, ix := range rep.Indexes {
			if ix.Name == sanitize.Line(profile, 64) {
				return fmt.Sprintf(" (%d document%s)", len(ix.Documents), plural(len(ix.Documents), "", "s")), nil
			}
		}
	}
	return "", nil
}
