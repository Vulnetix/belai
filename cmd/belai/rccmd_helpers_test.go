package main

import (
	"flag"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/rc"
)

// rcFlags is the slice of `belai rc`'s flag set the worker-cap helpers read.
func rcFlags(t *testing.T, args ...string) (fs *flag.FlagSet, max, maxWorkers, reserved *int) {
	t.Helper()
	fs = flag.NewFlagSet("rc", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	max = fs.Int("max", 4, "")
	maxWorkers = fs.Int("max-workers", 0, "")
	reserved = fs.Int("factory-reserved-workers", 0, "")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return fs, max, maxWorkers, reserved
}

func TestFlagGivenReportsOnlyFlagsOnTheCommandLine(t *testing.T) {
	fs, _, _, _ := rcFlags(t, "--max", "4", "--factory-reserved-workers=0")
	for name, want := range map[string]bool{
		"max":                      true, // given, even at its default value
		"factory-reserved-workers": true, // given as 0
		"max-workers":              false,
		"not-a-flag":               false,
	} {
		if got := flagGiven(fs, name); got != want {
			t.Errorf("flagGiven(%q) = %v, want %v", name, got, want)
		}
	}
	fs, _, _, _ = rcFlags(t)
	if flagGiven(fs, "max") {
		t.Error("flagGiven(max) with no arguments = true, want false")
	}
}

func TestWorkerOverridePrefersMaxWorkersThenMax(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"neither flag keeps agents.max_workers", nil, 0},
		{"the session default alone never sets the cap", []string{"--factory-reserved-workers", "2"}, 0},
		{"an explicit --max", []string{"--max", "6"}, 6},
		{"--max-workers alone", []string{"--max-workers", "3"}, 3},
		{"--max-workers wins over --max", []string{"--max", "6", "--max-workers", "3"}, 3},
		{"--max-workers wins in either order", []string{"--max-workers", "3", "--max", "6"}, 3},
		{"--max-workers 0 is still given", []string{"--max", "6", "--max-workers", "0"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, max, maxWorkers, _ := rcFlags(t, tc.args...)
			if got := workerOverride(fs, *max, *maxWorkers); got != tc.want {
				t.Fatalf("workerOverride(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestReservedOverrideIsNilUnlessGiven(t *testing.T) {
	fs, _, _, reserved := rcFlags(t, "--max", "6")
	if got := reservedOverride(fs, *reserved); got != nil {
		t.Fatalf("reservedOverride without the flag = %d, want nil", *got)
	}
	for _, n := range []string{"0", "3"} {
		fs, _, _, reserved := rcFlags(t, "--factory-reserved-workers", n)
		got := reservedOverride(fs, *reserved)
		if got == nil {
			t.Fatalf("reservedOverride with --factory-reserved-workers %s = nil", n)
		}
		if want := *reserved; *got != want {
			t.Fatalf("reservedOverride with --factory-reserved-workers %s = %d, want %d", n, *got, want)
		}
	}
}

func rcRecord(dirs int) rc.Record {
	started := time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)
	r := rc.Record{
		PID:     4242,
		Started: started,
		URL:     "https://example.test/hosts",
		Max:     3,
		Online:  true,
		Sessions: []rc.RunningSession{
			{ID: "0123456789abcdef", Cwd: "/work/alpha", Started: started.Add(5 * time.Minute)},
			{ID: "fedcba9876543210", Cwd: "/work/beta", Started: started.Add(65 * time.Minute)},
		},
	}
	for i := 0; i < dirs; i++ {
		r.Dirs = append(r.Dirs, rc.Dir{Path: "/work/d", Name: "d"})
	}
	return r
}

func TestRCStatusTextNotRunning(t *testing.T) {
	got := rcStatusText(rcRecord(1), false, "https://example.test/manage")
	want := "belai rc is not running. Start it with `belai rc` (or /rc in Belai).\nManage hosts: https://example.test/manage\n"
	if got != want {
		t.Fatalf("not running:\n got %q\nwant %q", got, want)
	}
}

func TestRCStatusTextRunning(t *testing.T) {
	r := rcRecord(2)
	r.LogPath = "/var/log/rc.log"
	got := rcStatusText(r, true, "https://unused.test")
	for _, want := range []string{
		"belai rc is running (pid 4242, since " + r.Started.Format(time.RFC822) + ") · connected\n",
		"sessions: 2 of 3\n",
		"  01234567  /work/alpha  started 09:35\n",
		"  fedcba98  /work/beta  started 10:35\n",
		"offering 2 directories\n",
		"manage hosts: https://example.test/hosts\n",
		"log: /var/log/rc.log\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
	// Session ids are cut to their 8-character prefix.
	if strings.Contains(got, "0123456789abcdef") {
		t.Errorf("status shows a full session id:\n%s", got)
	}
	// The url argument is for the not-running text; a running daemon names
	// its own.
	if strings.Contains(got, "unused.test") {
		t.Errorf("running status used the argument url:\n%s", got)
	}
}

func TestRCStatusTextConnectionState(t *testing.T) {
	cases := []struct {
		name   string
		online bool
		err    string
		want   string
	}{
		{"online", true, "", "· connected\n"},
		{"offline without a cause", false, "", "· not connected\n"},
		{"offline with a cause", false, "heartbeat refused", "· not connected (heartbeat refused)\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rcRecord(1)
			r.Online, r.Error = tc.online, tc.err
			got := rcStatusText(r, true, "")
			first, _, _ := strings.Cut(got, "\n")
			if !strings.HasSuffix(first+"\n", tc.want) {
				t.Fatalf("first line %q, want suffix %q", first, tc.want)
			}
		})
	}
}

func TestRCStatusTextDirectoryPlural(t *testing.T) {
	for dirs, want := range map[int]string{
		0: "offering 0 directories\n",
		1: "offering 1 directory\n",
		2: "offering 2 directories\n",
	} {
		r := rcRecord(dirs)
		r.Sessions = nil
		got := rcStatusText(r, true, "")
		if !strings.Contains(got, want) {
			t.Errorf("%d dirs: status lacks %q:\n%s", dirs, want, got)
		}
		if !strings.Contains(got, "sessions: 0 of 3\n") {
			t.Errorf("%d dirs: status lacks the empty session count:\n%s", dirs, got)
		}
		if strings.Contains(got, "log:") {
			t.Errorf("%d dirs: a record without a log path printed one:\n%s", dirs, got)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"short line is kept", "fix the tests", 60, "fix the tests"},
		{"surrounding space is trimmed", "  \n\t fix the tests  \n", 60, "fix the tests"},
		{"only the first line", "first line\nsecond line", 60, "first line"},
		{"trailing space before the newline", "first   \nsecond", 60, "first"},
		{"exactly n runes is kept", "abcde", 5, "abcde"},
		{"over n runes is capped with an ellipsis", "abcdef", 5, "abcd…"},
		{"the cap counts runes, not bytes", "ééééééé", 4, "ééé…"},
		{"empty", "", 10, ""},
		{"whitespace only", " \n ", 10, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLine(tc.in, tc.n); got != tc.want {
				t.Fatalf("firstLine(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}
