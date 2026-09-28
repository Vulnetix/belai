package rc

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

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
	cwd, ok := Allowed(d.o.Dirs, r.Cwd)
	if !ok {
		return "", "this host does not offer that directory"
	}
	w := WorkerStart{Exe: d.o.Exe, Cwd: cwd}
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
// trust check, the preflight and the agents.max_workers check, and waits
// briefly for the workers to register.
func runAgentStart(w WorkerStart) (string, error) {
	args := []string{"agent", "start"}
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
