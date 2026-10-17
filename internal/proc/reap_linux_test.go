//go:build linux

package proc

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseStat(t *testing.T) {
	// A command name with spaces and a closing parenthesis must not shift the fields.
	line := "123 (a b) c) Z 45 123 123 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 987654 0 0 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0"
	ppid, start, zombie := parseStat(line)
	if ppid != 45 || start != "987654" || !zombie {
		t.Fatalf("parseStat = %d %q %v", ppid, start, zombie)
	}
	if _, _, z := parseStat("garbage"); z {
		t.Fatal("garbage read as a zombie")
	}
}

func TestReapOrphansCollectsAdoptedZombie(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ReapOrphans(ctx, 50*time.Millisecond, nil)
	time.Sleep(100 * time.Millisecond) // the subreaper flag is set before the orphan exists

	// The shell starts a grandchild and exits at once, so the grandchild is
	// adopted by this process; it ends 300ms later and nobody waits for it.
	out, err := exec.Command("sh", "-c", "sleep 0.3 & echo $!").Output()
	if err != nil {
		t.Skipf("no shell: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("adopted process %d still in the process table", pid)
}
