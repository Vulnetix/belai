package voice

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/proc"
	"github.com/vulnetix/belai/internal/sanitize"
)

// probe runs one diagnostic program with a fixed argv and the scrubbed
// environment, and returns its standard output. It is a variable so tests can
// stand in for the programs.
var probe = func(ctx context.Context, name string, args ...string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

const (
	maxDeviceLines = 8
	maxDeviceCols  = 110
)

// Devices lists what the sound system offers to record from, for /voice
// debug: the default input and the inputs PulseAudio or PipeWire report, or
// ALSA's cards when neither is installed. The programs are fixed and take no
// argument from settings or the model, and every line is cleaned and capped.
// An empty result means no probe program is installed.
func Devices(ctx context.Context) []string {
	var lines []string
	if out, err := probe(ctx, "pactl", "get-default-source"); err == nil {
		if name := firstLine(out); name != "" {
			lines = append(lines, "default input: "+name)
		}
	}
	if out, err := probe(ctx, "pactl", "list", "short", "sources"); err == nil {
		lines = append(lines, sourceLines(out)...)
	}
	if len(lines) == 0 {
		if out, err := probe(ctx, "arecord", "-l"); err == nil {
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "card ") {
					lines = append(lines, "alsa "+l)
				}
			}
		}
	}
	if len(lines) > maxDeviceLines {
		lines = lines[:maxDeviceLines]
	}
	for i, l := range lines {
		lines[i] = sanitize.Line(l, maxDeviceCols)
	}
	return lines
}

// sourceLines turns `pactl list short sources` into one line per input:
// its name and state, with a note on monitors, which carry the sound the
// computer plays rather than a microphone.
func sourceLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		state := f[len(f)-1]
		line := "input: " + f[1] + " (" + strings.ToLower(state) + ")"
		if strings.HasSuffix(f[1], ".monitor") {
			line += " - plays back an output, not a microphone"
		}
		lines = append(lines, line)
	}
	return lines
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// Command is the capture helper's program and arguments as one line, for
// /voice debug. It carries no secret: the argv is fixed except for the
// validated device name.
func (s *ExecSource) Command() string {
	return sanitize.Line(s.Name()+" "+strings.Join(s.args, " "), 200)
}
