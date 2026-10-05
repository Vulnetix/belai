package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	if _, err := agentprofile.Load(res.Profile.Name); err == nil || !errors.Is(err, fs.ErrNotExist) {
		// A profile that exists, or one that is on disk but will not load, is not
		// replaced unless the user says so.
		if !force {
			return 1, fmt.Errorf("a profile named %q exists; pass -force to replace it, or -name to save it under another name", res.Profile.Name)
		}
	}

	// A skill that came with the definition is installed only when the host has
	// no skill of that name: -force is for the profile, and never replaces a skill
	// the user wrote. A skill that will not be installed is also left out of the
	// profile's skills, so the profile never names a skill that is not there.
	var install, skipped []string
	byName := map[string]agentimport.Skill{}
	var kept []string
	for _, sk := range res.Skills {
		byName[sk.Name] = sk
		if _, err := libstore.Get(libitem.Skill, sk.Name); err == nil {
			skipped = append(skipped, sk.Name)
			continue
		}
		install = append(install, sk.Name)
		kept = append(kept, sk.Name)
	}
	res.Profile.Skills = kept
	if len(skipped) > 0 {
		fmt.Fprintf(stdout, "\nSkills the host already has, not replaced and not named in the profile: %s\n", strings.Join(skipped, ", "))
	}
	if !yes {
		fmt.Fprintf(stdout, "\nPreview only. Nothing was saved. Run the same command with -yes to save profile %q", res.Profile.Name)
		if len(install) > 0 {
			fmt.Fprintf(stdout, " and install %d skill(s)", len(install))
		}
		fmt.Fprintln(stdout, ".")
		return 0, nil
	}
	// The profile is saved first, so a refusal (a reserved or colliding name)
	// leaves the host as it was; the skills follow.
	saved, err := agentprofile.Save(res.Profile)
	if err != nil {
		return 1, err
	}
	fmt.Fprintf(stdout, "saved %s\n", saved)
	for _, name := range install {
		if _, err := libstore.Install(libitem.Skill, byName[name].Doc, libstore.InstallOptions{}); err != nil {
			fmt.Fprintf(stderr, "skill %s was not installed: %v (the profile names it; install it yourself or remove it from the profile)\n", name, err)
			continue
		}
		fmt.Fprintf(stdout, "installed skill %s\n", name)
	}
	return 0, nil
}

func printImportReport(w io.Writer, res agentimport.Result) {
	p := res.Profile
	fmt.Fprintf(w, "Imported a %s definition as profile %q (single mode, supervised).\n", res.Format, p.Name)
	fmt.Fprintf(w, "  tools: %s\n", strings.Join(p.Tools, ", "))
	if len(res.MutatingTools) > 0 {
		fmt.Fprintf(w, "  these tools can change files or run commands: %s (each call still asks under your permission rules)\n", strings.Join(res.MutatingTools, ", "))
	}
	var egress []string
	for _, t := range p.Tools {
		if t == "WebFetch" || t == "WebSearch" {
			egress = append(egress, t)
		}
	}
	if len(egress) > 0 {
		fmt.Fprintf(w, "  these tools send text off this machine: %s\n", strings.Join(egress, ", "))
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
