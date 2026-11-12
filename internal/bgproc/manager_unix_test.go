//go:build !windows

package bgproc

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

func TestShutdownKillsGroup(t *testing.T) {
	m := testManager(t)
	// Two children that would outlive a plain Kill on the parent shell. The
	// process group kill should take them down. Each child's pid is logged so
	// the test can wait for both to exist and then check each one is gone.
	script := "sleep 30 & echo $!; sleep 30 & echo $!; wait"
	p, err := m.Start("sleeper", script)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var pids []int
	eventually(func() bool {
		tail, _ := m.TailByID(p.ID, 10)
		pids = pids[:0]
		for _, f := range strings.Fields(tail) {
			if n, err := strconv.Atoi(f); err == nil {
				pids = append(pids, n)
			}
		}
		return len(pids) == 2
	})
	if len(pids) != 2 {
		t.Fatalf("the script never logged both child pids (%v)", pids)
	}
	pids = append(pids, p.PID)

	m.Shutdown()

	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for running(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d of group %d still alive after Shutdown", pid, p.PID)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// running is proc.Alive with a zombie counted as gone: a zombie has exited
// and only waits for its parent (here, whatever adopted an orphaned child) to
// reap it, which this test does not control.
func running(pid int) bool {
	return proc.Alive(pid) && !zombie(pid)
}

// zombie reads the process state from procfs. Without procfs (macOS) it
// reports false, leaving proc.Alive's answer.
func zombie(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state is the field after the parenthesised command name.
	i := bytes.LastIndexByte(stat, ')')
	return i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z'
}
