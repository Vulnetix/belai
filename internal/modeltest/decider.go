package modeltest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/deciderserver"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/localinfer"
	"github.com/vulnetix/belai/internal/run"
)

// DeciderTarget is Strands Decider on this machine. Opts reaches the server
// helpers unchanged; tests point its Root and Ports at fixtures.
type DeciderTarget struct {
	Model   decisions.DeciderModel
	Timeout time.Duration
	Opts    deciderserver.Options
}

// StrandsDeciderSteps is the ladder for Strands Decider-2B on this machine:
// a server already answering on loopback is used as it is; otherwise the
// strands-decider command must be installed, the pinned weights are
// downloaded after the user confirms, and the server is started. Then the
// sanity pair, intent and a choice with object criteria, which is the
// request shape only this server requires.
func StrandsDeciderSteps(t DeciderTarget) []Step {
	m := t.Model
	var d *decisions.SystemOne
	var running string
	opts := func(st *State) deciderserver.Options {
		o := t.Opts
		if o.Client == nil {
			o.Client = st.Env.Client
		}
		if o.Registry == nil {
			o.Registry = st.Env.Registry
		}
		if o.OnLine == nil {
			o.OnLine = st.log
		}
		if o.Deadline == 0 {
			o.Deadline = st.Env.LocalDeadline
		}
		return o
	}
	return []Step{
		{Name: "strands-decider", Run: func(ctx context.Context, st *State) Outcome {
			if base, h, up := deciderserver.Running(ctx, opts(st)); up {
				running = base
				if h.Device != "" {
					return ok("a server is answering at %s on %s", strings.TrimPrefix(base, "http://"), h.Device)
				}
				return ok("a server is answering at %s", strings.TrimPrefix(base, "http://"))
			}
			bin, found := deciderserver.Binary()
			if !found {
				return fail("strands-decider is not on PATH and no server is answering on loopback", installDeciderHint(), retryHint())
			}
			return ok("found %s", bin.Path)
		}},
		{Name: "weights", Run: func(ctx context.Context, st *State) Outcome {
			if running != "" {
				return skip("the running server holds its own weights")
			}
			return ensureDeciderWeights(ctx, st, m, opts(st))
		}},
		{Name: "start", Run: func(ctx context.Context, st *State) Outcome {
			h, err := deciderserver.Ensure(ctx, m, opts(st))
			if err != nil {
				return deciderLaunchFailure(err)
			}
			st.Decider = h
			d = &decisions.SystemOne{
				Name: decisions.DeciderProvider, Model: m.ID, BaseURL: h.BaseURL,
				Client: st.Env.Client, Timeout: t.Timeout, CriteriaObject: true, MaxOptions: m.MaxOptions,
			}
			if h.Owned {
				return ok("started on port %d", h.Port)
			}
			return ok("using the running server on port %d", h.Port)
		}},
		{Name: "answer", Run: func(ctx context.Context, st *State) Outcome {
			res, err := d.Decide(ctx, decisions.Request{
				State:     map[string]any{"content": benignState},
				Questions: map[string]decisions.Question{"q": decisions.Noul(injectionProposition)},
			})
			if err != nil {
				return fail("no answer: "+oneLine(err.Error(), 160), retryHint())
			}
			o := ok("answered in %s", res.Meta.Latency.Round(time.Millisecond))
			o.Metrics = map[string]string{"latency": res.Meta.Latency.String()}
			return o
		}},
		sanityStep(func() decisions.Decider { return d }),
		intentStep(func() decisions.Decider { return d }),
		{Name: "choice", Run: func(ctx context.Context, st *State) Outcome {
			res, err := d.Decide(ctx, decisions.Request{
				State: map[string]any{"prompt": "The build fails with: undefined: parseConfig in cmd/main.go"},
				Questions: map[string]decisions.Question{"q": {
					Type: decisions.TypeChoice, Instructions: "Which kind of work does the user want next?",
					Options: []string{"agent", "plan", "debug"},
				}},
			})
			if err != nil {
				return fail("a choice without descriptions failed: "+oneLine(err.Error(), 160), retryHint())
			}
			a := res.Answers["q"]
			return ok("picked %q (%.2f) for a build failure", a.Choice, a.Probabilities[a.Choice])
		}},
	}
}

// ensureDeciderWeights downloads the pinned checkpoint and base model after
// the user confirms the size.
func ensureDeciderWeights(ctx context.Context, st *State, m decisions.DeciderModel, o deciderserver.Options) Outcome {
	root := o.Root
	if root == "" {
		var err error
		if root, err = deciderserver.Root(); err != nil {
			return fail("no models directory: " + oneLine(err.Error(), 120))
		}
	}
	if deciderserver.Present(root, m) {
		return ok("%s is on disk", m.Label)
	}
	need := deciderserver.Missing(root, m)
	offer := DownloadOffer{Label: m.Label, What: m.Repo + " + " + m.BaseRepo, Size: need, Dest: root}
	if !st.confirm(ctx, offer) {
		if ctx.Err() != nil {
			return fail("cancelled")
		}
		return fail(fmt.Sprintf("download of %s (%s) declined, so it was not saved", m.Label, Sizes(need)),
			Hint{Key: "r", Text: "ask again", Action: ActRetry})
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		err = deciderserver.Download(ctx, st.Env.Client, root, m, st.Env.HFToken, st.progress)
		var de *localinfer.DownloadError
		if err == nil || ctx.Err() != nil || !errors.As(err, &de) || de.Class != localinfer.DownloadChecksum {
			break
		}
		st.log("a file failed its checksum and was removed; downloading again")
	}
	if err != nil {
		if ctx.Err() != nil {
			return fail("download cancelled; it resumes where it stopped next time")
		}
		out, _ := downloadFailure(err, m.Repo)
		return out
	}
	return ok("downloaded %s (%s)", m.Label, Sizes(need))
}

func deciderLaunchFailure(err error) Outcome {
	switch {
	case errors.Is(err, deciderserver.ErrNoBinary):
		return fail("strands-decider is not on PATH", installDeciderHint(), retryHint())
	case errors.Is(err, deciderserver.ErrWeightsMissing):
		return fail("the model files are missing", retryHint())
	}
	var le *localinfer.LaunchError
	if errors.As(err, &le) {
		tail := logTail(le.Output, 2)
		if le.Class == localinfer.LogOOM {
			return fail("the model does not fit in memory: "+tail, Hint{Text: "close other large programs and retry"}, retryHint())
		}
		if le.ExitCode >= 0 {
			return fail(fmt.Sprintf("strands-decider exited during startup (code %d): %s", le.ExitCode, tail),
				Hint{Text: "update it (uv tool upgrade strands-decider); it needs transformers 5.15 or later for Qwen3.5"}, retryHint())
		}
		return fail("strands-decider did not finish loading in time: "+tail,
			Hint{Text: "a first load on a CPU reads 4.5 GB and can take minutes; retry"}, retryHint())
	}
	return fail("could not start strands-decider: "+oneLine(err.Error(), 160), retryHint())
}

// HostedClefSteps is the ladder for Clef on Workers AI, directly or through
// AI Gateway. The endpoint is fixed by the resolver, so nothing else on
// Cloudflare's hosts is probed: one answer, the sanity pair, intent, a choice
// with object criteria, and the time an answer takes.
func HostedClefSteps(d run.DecisionsConfig) []Step {
	var s *decisions.SystemOne
	build := func(st *State) *decisions.SystemOne {
		if s == nil {
			dec, _ := d.NewDecider(st.Env.Client).(*decisions.SystemOne)
			s = dec
		}
		return s
	}
	name := "clef · " + d.Provider
	return []Step{
		{Name: name, Run: func(ctx context.Context, st *State) Outcome {
			c := build(st)
			if c == nil {
				return fail("Clef is not configured", providersHint("set the Cloudflare account id and API token under providers → "+d.Provider))
			}
			res, err := c.Decide(ctx, decisions.Request{
				State:     map[string]any{"content": benignState},
				Questions: map[string]decisions.Question{"q": decisions.Noul(injectionProposition)},
			})
			if err != nil {
				switch decisions.ClassOf(err) {
				case decisions.ClassAuth:
					return fail("Cloudflare refused the credential: "+oneLine(err.Error(), 160), providersHint("check the API token (Workers AI read and run) under providers → "+d.Provider), retryHint())
				case decisions.ClassNotFound:
					return fail("Cloudflare does not know this model or account: "+oneLine(err.Error(), 160), providersHint("check the account id under providers → "+d.Provider), retryHint())
				}
				return fail("no answer: "+oneLine(err.Error(), 160), retryHint())
			}
			o := ok("answered in %s", res.Meta.Latency.Round(time.Millisecond))
			o.Metrics = map[string]string{"latency": res.Meta.Latency.String()}
			return o
		}},
		sanityStep(func() decisions.Decider { return s }),
		intentStep(func() decisions.Decider { return s }),
		{Name: "choice", Run: func(ctx context.Context, st *State) Outcome {
			res, err := s.Decide(ctx, decisions.Request{
				State: map[string]any{"prompt": "The build fails with: undefined: parseConfig in cmd/main.go"},
				Questions: map[string]decisions.Question{"q": {
					Type: decisions.TypeChoice, Instructions: "Which kind of work does the user want next?",
					Options: []string{"agent", "plan", "debug"},
				}},
			})
			if err != nil {
				return warn("a choice failed (" + oneLine(err.Error(), 120) + "); jobs that ask choices fall back to their ordinary behaviour")
			}
			a := res.Answers["q"]
			return ok("picked %q (%.2f) for a build failure", a.Choice, a.Probabilities[a.Choice])
		}},
	}
}
