package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/httpclient"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/teleport"
	"github.com/vulnetix/belai/internal/teleport/replay"
	"github.com/vulnetix/belai/internal/tools"
)

// replayTools is the whole tool surface a teleport replay runs with: reading
// and editing files in the replay's worktree, and nothing that runs a command or
// reaches the network. The hand-over came from another host's model, so no word
// in it can start a process: even if an instruction asked for one, the tool is
// not there to call. Code mode adds only the Code tool, whose nested calls reach
// this set and no other.
var replayTools = []string{"Read", "Write", "Edit", "Grep", "Glob", "LS"}

// runTeleportReplay finishes a teleport's code on this host when the forge could
// not carry it (docs/teleport.md "Code"). The harness applies the patch with git
// and checks it against the origin's tree; for what is left, this host's own model
// runs in code mode with the origin's hand-over as a gated attachment, and a
// decision model (with a model sentinel fallback) rates the result against the
// origin's summary. It runs before the terminal UI opens, so the user waits at
// the terminal with progress lines, and it never fails the teleport: the outcome
// is a notice the session opens with.
func runTeleportReplay(ctx context.Context, rp *teleport.Replay, providerFlag, modelFlag string, stderr io.Writer) replay.Outcome {
	say := func(s string) { fmt.Fprintln(stderr, "teleport:", s) }
	o := replay.Options{Dir: rp.Dir, Plan: rp.Plan, Progress: say}

	agentSess, judge, cleanup, err := replaySession(ctx, rp.Dir, providerFlag, modelFlag)
	if err != nil {
		say("no model is available here, so only git's own application of the patch can run: " + err.Error())
	} else {
		defer cleanup()
		o.Agent, o.Judge = agentSess, judge
	}
	out, rerr := replay.Run(ctx, o)
	if rerr != nil {
		return replay.Outcome{Summary: "The replay could not run: " + rerr.Error() + ". The worktree holds nothing from the origin's changes."}
	}
	return out
}

// replaySession builds the model session and the verification judge for a
// replay in dir, narrowed to replayTools, with asks off (nobody can answer one
// mid-replay) and the host's own guardrails, posture and permission rules.
func replaySession(ctx context.Context, dir, providerFlag, modelFlag string) (replay.Runner, replay.Judge, func(), error) {
	settings, pol, err := repoPolicy(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	resolver, err := newResolver(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	wantProvider, wantModel := workerModel(providerFlag, modelFlag, settings, config.LoadState)
	cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
	if err != nil {
		return nil, nil, nil, err
	}
	if cfg, err = withClassifier(cfg, settings, resolver); err != nil {
		return nil, nil, nil, err
	}
	if err := run.PreloadClassifier(run.ResolveSecurityClassifier(settings.Classifier)); err != nil {
		return nil, nil, nil, fmt.Errorf("load embedded classifier: %w", err)
	}
	client := httpclient.Default()
	id, err := session.NewID()
	if err != nil {
		return nil, nil, nil, err
	}
	sess, err := headless.NewSession(ctx, headless.Params{
		Cfg: cfg, Client: client, Posture: pol, Workdir: dir, Settings: settings,
		SessionID: id, AllowAsk: false, MCP: mcp.Active(),
		Narrow: func(r *tools.Registry) *tools.Registry { return fleet.NarrowTools(r, replayTools) },
		Deny:   []string{"Write(*.vulnetix/*)", "Edit(*.vulnetix/*)"},
	})
	if err != nil {
		return nil, nil, nil, err
	}
	judge := replay.DecisionJudge{Jobs: run.NewJevJobs(cfg, settings.JevJobSet), Classifier: run.NewRoleClassifier(cfg, client, nil)}
	return sess, judge, func() {}, nil
}

// rcDistill is the origin host's hand-over writer: the fast model's
// teleport_distill role, resolved for each request so a settings change or a new
// login is picked up without restarting the daemon. Every failure is the harness
// fallback, never an error: a teleport never waits on a model that is not there.
func rcDistill(wd string) func(context.Context, []rolemanager.TeleportFile, []string, string) rolemanager.Distilled {
	return func(ctx context.Context, files []rolemanager.TeleportFile, skipped []string, patch string) rolemanager.Distilled {
		fallback := func() rolemanager.Distilled { return rolemanager.DistillTeleport(ctx, nil, files, skipped, patch) }
		repo := repoRoot(wd)
		settings, _, err := repoPolicy(repo)
		if err != nil {
			return fallback()
		}
		resolver, err := newResolver(repo)
		if err != nil {
			return fallback()
		}
		wantProvider, wantModel := workerModel("", "", settings, config.LoadState)
		cfg, err := run.ResolveWithSource(wantModel, wantProvider, os.Getenv, resolver)
		if err != nil {
			return fallback()
		}
		if cfg, err = withClassifier(cfg, settings, resolver); err != nil {
			return fallback()
		}
		return rolemanager.DistillTeleport(ctx, run.NewRoleClassifier(cfg, httpclient.Default(), nil), files, skipped, patch)
	}
}

// teleportPushPolicy reads this host's teleport.push from its own settings each
// time a request arrives, so a change applies without restarting the daemon. An
// unreadable settings file is "never": a host that cannot say does not push.
func teleportPushPolicy(wd string) func() string {
	return func() string {
		settings, err := config.LoadMerged(repoRoot(wd))
		if err != nil {
			return config.TeleportPushNever
		}
		return settings.TeleportPushPolicy()
	}
}
