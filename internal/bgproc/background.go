package bgproc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/tools"
)

// DefaultMaxBackground is how many model-started processes may run at once
// when the settings name no other cap.
const DefaultMaxBackground = 8

// maxBackgroundKept bounds finished model-started processes kept so their
// output stays readable; the oldest finished one is dropped past it.
const maxBackgroundKept = 16

// StartBackground launches a process the model asked for. It differs from a
// user's !!cmd in three ways: it runs under the policy of the call that asked
// (the same OS sandbox as Bash), it is never handed to the recovery subagent
// when it exits, and only the background tools can read or stop it.
func (m *Manager) StartBackground(command, dir string, policy sandbox.Policy) (tools.BackgroundInfo, error) {
	if strings.TrimSpace(command) == "" {
		return tools.BackgroundInfo{}, fmt.Errorf("empty command")
	}
	if dir == "" {
		dir = m.workdir
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	limit := m.settings.Resilience.MaxBackgroundOr(DefaultMaxBackground)
	running := 0
	var finished []*processInstance
	for _, p := range m.procs {
		if !p.background {
			continue
		}
		if p.state == StateRunning {
			running++
		} else {
			finished = append(finished, p)
		}
	}
	if running >= limit {
		return tools.BackgroundInfo{}, fmt.Errorf("%d background processes are already running (the cap); stop one with KillShell first", running)
	}
	if len(finished) >= maxBackgroundKept {
		sort.Slice(finished, func(i, j int) bool { return finished[i].ended.Before(finished[j].ended) })
		for _, old := range finished[:len(finished)-maxBackgroundKept+1] {
			_ = old.log.Close()
			delete(m.procs, old.id)
		}
	}

	id := fmt.Sprintf("p%d", m.nextID)
	m.nextID++
	slug := "bg-" + id

	logPath := filepath.Join(m.logsDir, fmt.Sprintf("%d-%s-%s.log", time.Now().Unix(), slug, id))
	logW, err := newCappedLogWriter(logPath, maxLogBytes)
	if err != nil {
		return tools.BackgroundInfo{}, fmt.Errorf("create log: %w", err)
	}
	lockPath := filepath.Join(m.logsDir, lockName(m.workdir, slug))
	if err := writeLock(lockPath); err != nil {
		_ = logW.Close()
		return tools.BackgroundInfo{}, fmt.Errorf("lock process: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &processInstance{
		id:         id,
		name:       slug,
		command:    command,
		dir:        dir,
		logPath:    logPath,
		lockPath:   lockPath,
		state:      StateRunning,
		started:    time.Now(),
		ctx:        ctx,
		cancel:     cancel,
		log:        logW,
		tail:       newRingBuffer(displayTailLines),
		background: true,
		policy:     policy,
	}
	m.procs[id] = p
	if err := m.startExecLocked(p); err != nil {
		_ = os.Remove(lockPath)
		_ = logW.Close()
		delete(m.procs, id)
		return tools.BackgroundInfo{}, err
	}
	m.pushEvent(Event{ID: id, Kind: "start", Process: p.snapshot()})
	return infoOf(p.snapshot()), nil
}

// backgroundProc returns the model-started process for id. A process the user
// started is reported as not found, so these tools cannot reach it.
func (m *Manager) backgroundProc(id string) (*processInstance, error) {
	m.mu.RLock()
	p, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok || !p.background {
		return nil, fmt.Errorf("background process %q not found", id)
	}
	return p, nil
}

// ReadBackground returns what the process wrote since the previous read.
func (m *Manager) ReadBackground(id, filter string, max int) (string, tools.BackgroundInfo, error) {
	p, err := m.backgroundProc(id)
	if err != nil {
		return "", tools.BackgroundInfo{}, err
	}
	var re *regexp.Regexp
	if filter != "" {
		re, err = regexp.Compile(filter)
		if err != nil {
			return "", tools.BackgroundInfo{}, fmt.Errorf("invalid regex: %w", err)
		}
	}
	if max <= 0 {
		max = 64 * 1024
	}
	p.log.Flush()

	p.mu.Lock()
	defer p.mu.Unlock()
	f, err := os.Open(p.logPath)
	if err != nil {
		return "", tools.BackgroundInfo{}, fmt.Errorf("open log: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(p.readOff, io.SeekStart); err != nil {
		return "", tools.BackgroundInfo{}, err
	}
	// One byte past the cap tells whether more is waiting.
	buf, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return "", tools.BackgroundInfo{}, err
	}
	more := len(buf) > max
	if more {
		buf = buf[:max]
		// Cut at a line end so a later read resumes on a line boundary.
		if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
			buf = buf[:i+1]
		}
	}
	p.readOff += int64(len(buf))

	text := string(buf)
	if re != nil {
		var keep []string
		sc := bufio.NewScanner(strings.NewReader(text))
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			if re.MatchString(sc.Text()) {
				keep = append(keep, sc.Text())
			}
		}
		text = strings.Join(keep, "\n")
	}
	if more {
		text += "\n… more output is waiting; call BashOutput again"
	}
	return text, infoOf(p.snapshotLocked()), nil
}

// StopBackground stops a model-started process. Its output stays readable.
func (m *Manager) StopBackground(id string) (tools.BackgroundInfo, error) {
	p, err := m.backgroundProc(id)
	if err != nil {
		return tools.BackgroundInfo{}, err
	}
	if err := m.Stop(id); err != nil {
		return tools.BackgroundInfo{}, err
	}
	return infoOf(p.snapshot()), nil
}

// ListBackground returns the model-started processes still known, oldest first.
func (m *Manager) ListBackground() []tools.BackgroundInfo {
	m.mu.RLock()
	var snaps []Process
	for _, p := range m.procs {
		if p.background {
			snaps = append(snaps, p.snapshot())
		}
	}
	m.mu.RUnlock()
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Started.Before(snaps[j].Started) })
	out := make([]tools.BackgroundInfo, len(snaps))
	for i, s := range snaps {
		out[i] = infoOf(s)
	}
	return out
}

func infoOf(p Process) tools.BackgroundInfo {
	return tools.BackgroundInfo{ID: p.ID, State: string(p.State), ExitCode: p.ExitCode, Started: p.Started, Ended: p.Ended}
}

var _ tools.ProcessLauncher = (*Manager)(nil)
