//go:build linux

package proc

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// prSetChildSubreaper is PR_SET_CHILD_SUBREAPER from <linux/prctl.h>.
const prSetChildSubreaper = 36

// ReapOrphans makes this process the one that adopts the orphans of its
// descendants, and reaps the ones that have ended. Workers are started in their
// own session so they outlive the process that started them; when one ends,
// the nearest subreaper above it (else PID 1) must reap it. In a container
// whose PID 1 is not an init, nobody does, and every ended worker keeps a
// process-table slot until the cgroup's pids.max is full and the next fork
// fails with "resource temporarily unavailable".
//
// It returns once ctx ends, reporting through logf how many it reaped. A
// zombie is reaped only when it was already one on the previous pass: a child
// something is waiting on is reaped by that wait at once, so one that sits for a
// whole interval is an orphan nobody will collect.
func ReapOrphans(ctx context.Context, every time.Duration, logf func(format string, args ...any)) {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		return
	}
	var seen map[int]string
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var n int
		seen, n = reapPass("/proc", os.Getpid(), seen)
		if n > 0 && logf != nil {
			logf("reaped %d ended process(es) adopted from workers", n)
		}
	}
}

// reapPass looks for zombies whose parent is parent, reaps those in prev (same
// pid and start time), and returns the zombies it saw now with the number it
// reaped.
func reapPass(procRoot string, parent int, prev map[int]string) (map[int]string, int) {
	now := map[int]string{}
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		return now, 0
	}
	reaped := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		raw, err := os.ReadFile(procRoot + "/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		ppid, start, zombie := parseStat(string(raw))
		if !zombie || ppid != parent {
			continue
		}
		if prev[pid] == start {
			var ws syscall.WaitStatus
			if wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); err == nil && wpid == pid {
				reaped++
				continue
			}
		}
		now[pid] = start
	}
	return now, reaped
}

// parseStat reads /proc/PID/stat: the parent, the start time and whether the
// state is Z. The command name is in parentheses and may hold spaces and
// parentheses, so the fields are counted from the last ")".
func parseStat(s string) (ppid int, start string, zombie bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, "", false
	}
	f := strings.Fields(s[i+1:])
	// f[0] state, f[1] ppid, ... f[19] starttime.
	if len(f) < 20 {
		return 0, "", false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, "", false
	}
	return ppid, f[19], f[0] == "Z"
}
