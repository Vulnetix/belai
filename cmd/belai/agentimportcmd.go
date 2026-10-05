package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/vulnetix/belai/internal/agentimport"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/libstore"
)

// agentImportForeign converts an agent definition written for another harness
// (see internal/agentimport), prints what became of each part of it, and, with
// -yes, saves the profile and installs the skills that came with it. Without
// -yes it is a preview and writes nothing.
func agentImportForeign(validateOnly bool, from, path, name string, yes, force bool, stdout, stderr io.Writer) (int, error) {
	f, err := agentimport.ParseFormat(from)
	if err != nil {
		return 2, err
	}
	res, err := agentimport.Import(path, f, agentimport.Options{Name: name})
	if err != nil {
		return 1, fmt.Errorf("%s: %w", path, err)
	}
	printImportReport(stdout, res)
	if validateOnly {
		fmt.Fprintf(stdout, "\n%s: a valid %s profile %q\n", path, res.Format, res.Profile.Name)
		return 0, nil
	}
	if _, err := agentprofile.Load(res.Profile.Name); err == nil && !force {
		return 1, fmt.Errorf("a profile named %q exists; pass -force to replace it, or -name to save it under another name", res.Profile.Name)
	}
	if !yes {
		fmt.Fprintf(stdout, "\nPreview only. Nothing was saved. Run the same command with -yes to save profile %q", res.Profile.Name)
		if len(res.Skills) > 0 {
			fmt.Fprintf(stdout, " and install %d skill(s)", len(res.Skills))
		}
		fmt.Fprintln(stdout, ".")
		return 0, nil
	}
	for _, s := range res.Skills {
		if _, err := libstore.Install(libitem.Skill, s.Doc, libstore.InstallOptions{Overwrite: force}); err != nil {
			if libstore.IsRefusal(err) {
				fmt.Fprintf(stderr, "skill %s not installed: %v\n", s.Name, err)
				continue
			}
			return 1, err
		}
		fmt.Fprintf(stdout, "installed skill %s\n", s.Name)
	}
	saved, err := agentprofile.Save(res.Profile)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "saved %s\n", saved)
	return 0, nil
}

func printImportReport(w io.Writer, res agentimport.Result) {
	p := res.Profile
	fmt.Fprintf(w, "Imported a %s definition as profile %q (single mode, supervised).\n", res.Format, p.Name)
	fmt.Fprintf(w, "  tools: %s\n", strings.Join(p.Tools, ", "))
	if len(res.MutatingTools) > 0 {
		fmt.Fprintf(w, "  these tools can change files or run commands: %s (each call still asks under your permission rules)\n", strings.Join(res.MutatingTools, ", "))
	}
	if p.Provider != "" {
		fmt.Fprintf(w, "  model: %s/%s\n", p.Provider, p.Model)
	}
	for _, kind := range []agentimport.NoteKind{agentimport.Mapped, agentimport.Kept, agentimport.Dropped, agentimport.Warning} {
		var lines []string
		for _, n := range res.Notes {
			if n.Kind == kind {
				lines = append(lines, fmt.Sprintf("  - %s: %s", n.Field, n.Text))
			}
		}
		if len(lines) == 0 {
			continue
		}
		title := map[agentimport.NoteKind]string{
			agentimport.Mapped:  "Mapped",
			agentimport.Kept:    "Kept in the profile's metadata",
			agentimport.Dropped: "Not imported",
			agentimport.Warning: "Read before saving",
		}[kind]
		fmt.Fprintf(w, "\n%s:\n%s\n", title, strings.Join(lines, "\n"))
	}
}
