package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vulnetix/belai/internal/agentdraft"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
)

// draftClassifier builds the classifier `belai agent draft` drafts with. A
// test replaces it; the default resolves the repository's classifier model
// exactly as `belai agent run` resolves a worker's.
var draftClassifier = func(repo, providerName, model string) (rolemanager.Classifier, config.Settings, error) {
	settings, _, err := repoPolicy(repo)
	if err != nil {
		return nil, config.Settings{}, err
	}
	resolver, err := newResolver(repo)
	if err != nil {
		return nil, config.Settings{}, err
	}
	wantProvider, wantModel := workerModel(providerName, model, settings, config.LoadState)
	cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
	if err != nil {
		return nil, config.Settings{}, err
	}
	if cfg, err = withClassifier(cfg, settings, resolver); err != nil {
		return nil, config.Settings{}, err
	}
	return run.NewRoleClassifier(cfg, httpclient.Default(), nil), settings, nil
}

// agentDraft implements `belai agent draft [-json] [-o FILE] PREMISE`: the
// same drafter the website's agent builder asks a live host to run. Without
// -json it takes every offer and writes the profile as markdown, ready for
// `belai agent import`; with -json it prints the offers and their reasons.
// Crew offers go to stderr as notes, since a crew is a separate file.
func agentDraft(ctx context.Context, fs *flag.FlagSet, rest []string, stdout, stderr io.Writer, asJSON *bool) (int, error) {
	out := fs.String("o", "", "write the markdown to FILE instead of stdout")
	trust := fs.Bool("trust-dir", false, "trust the repository first")
	providerName := fs.String("provider", "", "provider override")
	model := fs.String("model", "", "model override")
	if err := parseInterleaved(fs, rest); err != nil {
		return 2, nil
	}
	premise := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if premise == "" {
		return 2, errors.New(`usage: belai agent draft [-json] [-o FILE] "what the agent should do"`)
	}
	wd, _ := os.Getwd()
	repo := repoRoot(wd)
	if err := trustRepo(repo, *trust); err != nil {
		return 1, err
	}
	cls, settings, err := draftClassifier(repo, *providerName, *model)
	if err != nil {
		return 1, err
	}
	d := agentdraft.Drafter{
		Classifier: cls,
		Caveman:    settings.ClassifierCavemanEnabled(),
		Workers: func() []agentdraft.Worker {
			ps, _ := agentprofile.List()
			return agentdraft.WorkersFrom(ps)
		},
		Crews: agentprofile.ListCrews,
	}
	res, err := d.Draft(ctx, agentdraft.Request{Premise: premise})
	if err != nil {
		return 1, err
	}
	if *asJSON {
		return 0, jsonOut(stdout, res)
	}
	p := agentdraft.Apply(res.Fields)
	md, err := agentprofile.MarshalMarkdown(p)
	if err != nil {
		return 1, err
	}
	if *out != "" {
		if err := os.WriteFile(*out, md, 0o600); err != nil {
			return 1, err
		}
		fmt.Fprintf(stderr, "wrote %s · load it with: belai agent import %s\n", *out, *out)
	} else if _, err := stdout.Write(md); err != nil {
		return 1, err
	}
	if verr := p.Validate(); verr != nil {
		fmt.Fprintf(stderr, "note: edit before importing, Belai would refuse it as drafted: %v\n", verr)
	}
	for _, c := range res.Crews {
		fmt.Fprintf(stderr, "crew offer: %s (%s)\n", c.Title, c.Why)
	}
	return 0, nil
}
