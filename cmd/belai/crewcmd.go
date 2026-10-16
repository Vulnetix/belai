package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/sanitize"
)

const crewUsage = `usage: belai crew log [-run ID|-crew NAME] [-follow] [-class C] [-worker W]
                      [-audit] [-verify] [-json] [-limit N]

Read a crew run's local log (~/.vulnetix/belai/crews/<run>/). With no -run
or -crew it reads the newest run. It prints the run's worker faults by
default, its crew-scoped audit entries with -audit, and whether the audit
chain is intact with -verify.

  -run ID      the run to read
  -crew NAME   the newest run of this crew
  -follow      keep printing new lines (polls every second)
  -class C     only faults of this class
  -worker W    only this worker's lines
  -audit       print audit entries instead of faults
  -verify      check the audit hash chain of the current file
  -json        print one JSON object per line
  -limit N     print at most the newest N lines (0: all)

See docs/fleet.md#crew-log.
`

// crewFollowEvery is the -follow poll interval.
var crewFollowEvery = time.Second

// runCrewCLI implements `belai crew …` and returns the exit code.
func runCrewCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	usage := func() { fmt.Fprint(stderr, crewUsage) }
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] != "log" {
		fmt.Fprintf(stderr, "belai crew: unknown command %q\n", args[0])
		usage()
		return 2
	}
	fs := flag.NewFlagSet("crew log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = usage
	run := fs.String("run", "", "the run to read")
	crew := fs.String("crew", "", "the newest run of this crew")
	follow := fs.Bool("follow", false, "keep printing new lines")
	class := fs.String("class", "", "only faults of this class")
	worker := fs.String("worker", "", "only this worker's lines")
	auditMode := fs.Bool("audit", false, "print audit entries instead of faults")
	verify := fs.Bool("verify", false, "check the audit hash chain")
	asJSON := fs.Bool("json", false, "print JSON lines")
	limit := fs.Int("limit", 0, "print at most the newest N lines")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*run != "" && *crew != "") {
		usage()
		return 2
	}
	global, err := config.GlobalDir()
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	dir, id, err := crewRunDir(global, *run, *crew)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	if *verify {
		return crewVerify(dir, id, *asJSON, stdout, stderr)
	}
	f := crewlog.Filter{Class: audit.Clean(*class), Worker: audit.Clean(*worker), Limit: *limit}
	if err := crewLogPrint(ctx, dir, f, *auditMode, *follow, *asJSON, stdout); err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	return 0
}

// crewRunDir picks the run to read: the one named, the newest of a crew, or
// the newest of all.
func crewRunDir(global, run, crew string) (dir, id string, err error) {
	if run != "" {
		dir = crewlog.Dir(global, run)
		if runs, _ := crewlog.Runs(global); !containsString(runs, filepath.Base(dir)) {
			return "", "", fmt.Errorf("no crew run %q", sanitize.Ident(run, 64))
		}
		return dir, filepath.Base(dir), nil
	}
	runs, err := crewlog.Runs(global)
	if err != nil {
		return "", "", err
	}
	if len(runs) == 0 {
		return "", "", errors.New("no crew runs yet")
	}
	if crew == "" {
		id = runs[len(runs)-1]
		return crewlog.Dir(global, id), id, nil
	}
	// A run belongs to a crew when its id starts with the crew's name or a
	// fault in it names the crew.
	want, prefix := audit.Clean(crew), sanitize.Ident(crew, 64)
	for i := len(runs) - 1; i >= 0; i-- {
		d := crewlog.Dir(global, runs[i])
		if prefix != "" && (runs[i] == prefix || strings.HasPrefix(runs[i], prefix+"-")) {
			return d, runs[i], nil
		}
		faults, _ := crewlog.ReadFaults(d, crewlog.Filter{})
		for _, f := range faults {
			if f.Crew == want {
				return d, runs[i], nil
			}
		}
	}
	return "", "", fmt.Errorf("no run of crew %q", want)
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func crewVerify(dir, id string, asJSON bool, stdout, stderr io.Writer) int {
	bad, err := crewlog.Verify(dir)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 1
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"run": id, "intact": bad == -1, "badSeq": bad})
	} else if bad == -1 {
		fmt.Fprintf(stdout, "%s: audit chain intact\n", id)
	} else {
		fmt.Fprintf(stdout, "%s: audit chain broken at seq %d\n", id, bad)
	}
	if bad != -1 {
		return 1
	}
	return 0
}

// crewLogPrint prints the run's faults or entries, then with follow polls for
// new ones until ctx ends. Only the first read is limited.
func crewLogPrint(ctx context.Context, dir string, f crewlog.Filter, entries, follow, asJSON bool, w io.Writer) error {
	enc := json.NewEncoder(w)
	// Faults have no sequence, so a poll prints those newer than the last time
	// printed, and those at that time it has not printed yet.
	var lastAt, lastSeq int64 = 0, -1
	seenAt := map[string]bool{}
	for {
		if entries {
			list, err := crewlog.ReadEntries(dir, f)
			if err != nil {
				return err
			}
			for _, e := range list {
				if e.Seq <= lastSeq {
					continue
				}
				lastSeq = e.Seq
				if asJSON {
					_ = enc.Encode(e)
				} else {
					fmt.Fprintln(w, crewEntryLine(e))
				}
			}
		} else {
			list, err := crewlog.ReadFaults(dir, f)
			if err != nil {
				return err
			}
			for _, x := range list {
				raw, _ := json.Marshal(x)
				if x.At < lastAt || (x.At == lastAt && seenAt[string(raw)]) {
					continue
				}
				if x.At > lastAt {
					lastAt, seenAt = x.At, map[string]bool{}
				}
				seenAt[string(raw)] = true
				if asJSON {
					_ = enc.Encode(x)
				} else {
					fmt.Fprintln(w, crewFaultLine(x))
				}
			}
		}
		if !follow {
			return nil
		}
		f.Limit = 0
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(crewFollowEvery):
		}
	}
}

func crewTime(at int64) string {
	return time.UnixMilli(at).Local().Format("2006-01-02 15:04:05")
}

// orDash shows an empty field as "-". Every field is cleaned again for the
// terminal: the file is on disk and could have been edited.
func orDash(s string) string {
	if s = sanitize.Line(s, 0); s == "" {
		return "-"
	}
	return s
}

func crewFaultLine(x crewlog.Fault) string {
	status := "-"
	if x.Status != 0 {
		status = strconv.Itoa(x.Status)
	}
	return fmt.Sprintf("%s  %-16s %-16s %-14s %-14s %4s  %s", crewTime(x.At), orDash(x.Worker), orDash(x.Profile),
		orDash(x.Item), orDash(x.Class), status, sanitize.Line(x.Detail, crewlog.MaxDetail))
}

func crewEntryLine(e crewlog.Entry) string {
	actor := e.Worker
	if actor == "" {
		actor = e.Actor
	}
	keys := make([]string, 0, len(e.Data))
	for k := range e.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var data []string
	for _, k := range keys {
		data = append(data, sanitize.Line(k, 32)+"="+sanitize.Line(e.Data[k], 0))
	}
	return fmt.Sprintf("%s  %5d  %-16s %-16s %-14s %-12s %s", crewTime(e.At), e.Seq, orDash(e.Kind), orDash(actor),
		orDash(e.Item), orDash(e.Outcome), strings.Join(data, " "))
}
