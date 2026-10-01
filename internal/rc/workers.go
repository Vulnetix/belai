package rc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	w := WorkerStart{Exe: d.o.Exe, Cwd: cwd, MaxWorkers: d.o.MaxWorkers, Drain: drain}
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
		w.Crew = r.Crew
	}
	report, err := d.o.StartWorkers(w)
	if err != nil {
		return "", clip(err.Error())
	}
	return clip(report), ""
}

// runAgentStart runs `belai agent start` in the directory, which does the
// trust check, the preflight and the agents.max_workers check (raised or
// lowered by belai rc --max), and waits briefly for the workers to register.
func runAgentStart(w WorkerStart) (string, error) {
	args := []string{"agent", "start"}
	if w.MaxWorkers > 0 {
		args = append(args, "-max-workers", strconv.Itoa(w.MaxWorkers))
	}
	if w.Drain {
		args = append(args, "-drain")
	}
	if w.Crew != "" {
		args = append(args, "-crew", w.Crew)
	} else {
		args = append(args, w.Profile)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.Exe, args...)
	cmd.Dir = w.Cwd
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

type startError string

func (e startError) Error() string { return string(e) }

// clip keeps the last line-joined maxStartReport bytes: the end of the
// command's output carries the result or the error.
func clip(s string) string {
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
