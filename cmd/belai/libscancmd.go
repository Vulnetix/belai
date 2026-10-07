package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/vulnetix/belai/internal/agentimport"
	"github.com/vulnetix/belai/internal/libscan"
)

const libraryScanUsage = `usage: belai library scan [-kind KIND] [-json]

Search this host for items kept by other agent harnesses (Claude Code, Cursor,
Codex and the rest) and by Belai, in your user directories and in the
repositories you trust, and say what an import would make of each one. Nothing is
written and nothing is sent. It is the report the website's Scan hosts action
asks for.

  -kind KIND   one of: %s (default: every kind)
  -json        print the report as JSON, the way it is uploaded

See docs/agent-import.md.
`

// runLibraryScanCLI implements `belai library scan` and returns the exit code.
func runLibraryScanCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var kinds []string
	for _, k := range agentimport.ItemKinds() {
		kinds = append(kinds, string(k))
	}
	usage := func() { fmt.Fprintf(stderr, libraryScanUsage, strings.Join(kinds, ", ")) }
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		usage()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] != "scan" {
		fmt.Fprintf(stderr, "belai library: unknown command %q\n", args[0])
		usage()
		return 2
	}
	fs := flag.NewFlagSet("library scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = usage
	kind := fs.String("kind", "", "scan one kind of item")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		usage()
		return 2
	}
	set, err := libscan.KindSet(*kind)
	if err != nil {
		fmt.Fprintln(stderr, "belai:", err)
		return 2
	}
	sctx, cancel := context.WithTimeout(ctx, libscan.DefaultBudget)
	defer cancel()
	rep := libscan.Scan(sctx, libscan.Options{Kinds: set})
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(stderr, "belai:", err)
			return 1
		}
		return 0
	}
	printScanReport(stdout, rep)
	return 0
}

func printScanReport(w io.Writer, r libscan.Report) {
	fmt.Fprintf(w, "Scanned %d locations in %d ms: %d item(s) found.\n", r.Locations, r.DurationMs, len(r.Items))
	if r.Partial {
		fmt.Fprintln(w, "The scan stopped at its time limit, so this report is partial.")
	}
	if len(r.Items) > 0 {
		fmt.Fprintln(w)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tNAME\tVERDICT\tHARNESS\tSCOPE\tPATH")
		for _, it := range r.Items {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.Kind, it.Name, it.Verdict, it.Harness, it.Scope, it.Path)
		}
		tw.Flush()
		var notes []string
		for _, it := range r.Items {
			if it.Reason != "" {
				notes = append(notes, fmt.Sprintf("  %s %s (%s): %s", it.Kind, it.Name, it.Verdict, it.Reason))
			}
		}
		if len(notes) > 0 {
			fmt.Fprintf(w, "\nRead before importing:\n%s\n", strings.Join(notes, "\n"))
		}
	}
	fmt.Fprintf(w, "\nHarnesses: %d checked, %d not installed.\n", r.Checked, r.NotInstalled)
	for _, h := range r.Harnesses {
		fmt.Fprintf(w, "  %s (%s) %s: %s\n", h.ID, h.Name, h.Home, countsText(h.Counts))
		for _, n := range h.Notes {
			fmt.Fprintf(w, "    %s\n", n)
		}
	}
	if len(r.Repos) > 0 {
		fmt.Fprintf(w, "\nTrusted repositories: %d.\n", len(r.Repos))
		for _, repo := range r.Repos {
			fmt.Fprintf(w, "  %s (%s): %s\n", repo.Path, repo.Name, countsText(repo.Found))
			if len(repo.Found) == 0 {
				fmt.Fprintf(w, "    looked in: %s\n", strings.Join(repo.Looked, ", "))
			}
		}
	}
	if r.Skipped > 0 {
		fmt.Fprintf(w, "\n%d path(s) were not read: links, credential files and files over the size limit.\n", r.Skipped)
	}
}

func countsText(c map[string]int) string {
	if len(c) == 0 {
		return "nothing found"
	}
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, c[k])
	}
	return strings.Join(parts, ", ")
}
