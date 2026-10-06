package jev

import (
	"context"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Jobs is what the agent uses to run the relevance jobs. It wraps a decision
// backend client and the settings that switch each job. A nil *Jobs, or one
// with no client, enables nothing.
type Jobs struct {
	// Client is the decision backend; nil disables every job.
	Client *Client
	// On reports whether a job's own switch is on. The caller builds it from
	// the live settings so a /settings change applies to the next call. Nil
	// means every job is on.
	On func(config.JevJob) bool
}

// Enabled reports whether the job may run: a backend exists and the job's
// switch is on.
func (j *Jobs) Enabled(job config.JevJob) bool {
	if j == nil || j.Client == nil {
		return false
	}
	return j.On == nil || j.On(job)
}

// Identity names the backend for records.
func (j *Jobs) Identity() string {
	if j == nil || j.Client == nil {
		return ""
	}
	return j.Client.Identity()
}

// SwapCandidate is one builtin tool that might replace a Bash command.
type SwapCandidate struct {
	// Name is the tool name.
	Name string
	// Description is the harness's one-line description of the tool.
	Description string
}

// swapCriterion is the shared statement every candidate is judged against.
const swapCriterion = "Calling the builtin tool named by the item once, with suitable arguments, produces exactly the output the shell command in state.context would print, and has no other effect. Answer true only if one call of that tool fully replaces the command."

// RateSwap rates how well each builtin tool would fully replace a shell
// command. The scores are probabilities in [0,1]; a candidate the backend did
// not answer is absent from the map. The command is model-written text and
// reaches the backend only as DecisionText.
func (j *Jobs) RateSwap(ctx context.Context, command string, cands []SwapCandidate) (map[string]float64, string, error) {
	items := make([]ScoreItem, 0, len(cands))
	for _, c := range cands {
		items = append(items, ScoreItem{ID: sanitize.Ident(c.Name, 64), Label: sanitize.ForDecision(c.Name+": "+c.Description, 240)})
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:       string(config.JevBashSwap),
		Criterion: sanitize.ForDecision(swapCriterion, 0),
		Context:   sanitize.ForDecision(command, 1500),
		Items:     items,
	})
	if err != nil {
		return nil, res.Identity, err
	}
	return res.Scores, res.Identity, nil
}

// PruneItem is one statement about a tool call or its result, to be judged
// against the conversation.
type PruneItem struct {
	// ID identifies the statement; the score is returned under it.
	ID string
	// Statement says what would be true if the call or result should stay.
	Statement string
}

// pruneCriterion is the shared statement every prune item is judged against.
const pruneCriterion = "The statement in the item is true, given the conversation in state.context and the user's goal in state.goal. A tool can always be run again, so a call or its output should stay only if it still matters for the work ahead."

// PrunePairs scores statements about tool calls and results against the
// conversation digest and the user's goal. An unanswered item is absent from
// the returned map.
func (j *Jobs) PrunePairs(ctx context.Context, state, goal sanitize.DecisionText, items []PruneItem) (map[string]float64, ScoreResult, error) {
	sitems := make([]ScoreItem, 0, len(items))
	for _, it := range items {
		sitems = append(sitems, ScoreItem{ID: it.ID, Label: sanitize.ForDecision(it.Statement, 320)})
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevPruneCompaction),
		Criterion:   sanitize.ForDecision(pruneCriterion, 0),
		Context:     state,
		Extra:       map[string]sanitize.DecisionText{"goal": goal},
		Items:       sitems,
		MaxRequests: 24,
	})
	return res.Scores, res, err
}

// optionQuestion is the shared question every option group is ranked by.
const optionQuestion = "Which option would the user most likely choose for the question in state.context? Consider what the request asks for and what the options mean."

// PickOptions ranks the options of one question by how likely the user is to
// choose each. question is the model's question and request the user's
// request for the turn; both are model- or user-written text and reach the
// backend only as DecisionText.
func (j *Jobs) PickOptions(ctx context.Context, question, request string, opts []ScoreItem) (PickResult, error) {
	return j.Client.Pick(ctx, PickRequest{
		Job:      string(config.JevOptionOrder),
		Question: sanitize.ForDecision(optionQuestion, 0),
		Context:  sanitize.ForDecision("Question: "+question+"\nUser request: "+request, 1500),
		Options:  opts,
	})
}

// SurfaceItem is a tool or skill to rate.
type SurfaceItem struct {
	// ID is the caller's key for the item.
	ID string
	// Name and Description describe it; the description is trimmed to one
	// sentence and cleaned.
	Name, Description string
}

// surfaceCriterion is the shared statement for tool and skill selection.
const surfaceCriterion = "Having the tool or skill in the item available would help with what state.context asks for. A tool that is only vaguely related, or that a different available tool covers better, does not."

// searchCriterion is the shared statement for ranking a ToolSearch query.
const searchCriterion = "Loading the tool or skill in the item would help with the search in state.context, given the user's request. Answer true only if it is a good match."

func surfaceItems(items []SurfaceItem) []ScoreItem {
	out := make([]ScoreItem, 0, len(items))
	for _, it := range items {
		label := it.Name
		if it.Description != "" {
			label += ": " + it.Description
		}
		out = append(out, ScoreItem{ID: it.ID, Label: sanitize.ForDecision(label, 240)})
	}
	return out
}

// RateSurface rates tools and skills against the user's request for the turn.
// Only the request and the mode reach the backend, with the candidates' names
// and one-line descriptions, as DecisionText.
func (j *Jobs) RateSurface(ctx context.Context, request, mode string, items []SurfaceItem) (map[string]float64, ScoreResult, error) {
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevToolSelection),
		Criterion:   sanitize.ForDecision(surfaceCriterion, 0),
		Context:     sanitize.ForDecision(request, 1500),
		Extra:       map[string]sanitize.DecisionText{"mode": sanitize.ForDecision(mode, 40)},
		Items:       surfaceItems(items),
		MaxRequests: 12,
	})
	return res.Scores, res, err
}

// RankSearch rates tools and skills against a ToolSearch query and the user's
// request.
func (j *Jobs) RankSearch(ctx context.Context, query, request string, items []SurfaceItem) (map[string]float64, ScoreResult, error) {
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevToolSearch),
		Criterion:   sanitize.ForDecision(searchCriterion, 0),
		Context:     sanitize.ForDecision("Search: "+query+"\nUser request: "+request, 1500),
		Items:       surfaceItems(items),
		MaxRequests: 8,
	})
	return res.Scores, res, err
}

// TriageAt is the score below which another edit pass is judged unlikely to
// clear the remaining diagnostics.
const TriageAt = 0.30

// repairCriterion is the statement rated for LSP triage.
const repairCriterion = "Another edit pass by the model on this file is likely to clear the errors listed in state.context, given how the earlier passes went. Answer false if the same errors keep coming back, if the pass count is high and the errors are not shrinking, or if the errors look like they need something outside this file."

// RateRepair rates how likely another edit pass is to clear a file's
// diagnostics. facts says how the attempts went and rows lists the errors;
// both are text derived from the user's code and reach the backend only as
// DecisionText. ok is false when the backend did not answer.
func (j *Jobs) RateRepair(ctx context.Context, facts, rows string) (p float64, res ScoreResult, ok bool) {
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevLSPTriage),
		Criterion:   sanitize.ForDecision(repairCriterion, 0),
		Context:     sanitize.ForDecision(facts+"\n"+rows, 2400),
		Items:       []ScoreItem{{ID: "fix", Label: sanitize.ForDecision("Another edit pass will clear the remaining errors in this file.", 0)}},
		MaxRequests: 2,
	})
	if err != nil {
		return 0, res, false
	}
	p, ok = res.Scores["fix"]
	return p, res, ok
}

// LocateItem is a directory or file to rate for explore_locate. The label is
// built by the locate package: a path, a size and a language, and declaration
// names only when previews are allowed for this backend.
type LocateItem struct {
	ID, Label string
}

// locateDirCriterion and locateFileCriterion are the shared statements for the
// two stages of a locate.
const (
	locateDirCriterion  = "The directory in the item is likely to hold code that answers the question in state.context. A directory that only shares a word with the question does not."
	locateFileCriterion = "The file in the item is likely to define or use the code that state.context asks about. Use the path, the language and the declared names."
)

// RateLocate rates directories, then files, against the question. stage is
// "dirs" or "files". The question is text the user wrote; the labels come from
// the locate package, which only ever puts paths and, if previews are allowed,
// declared names in them. Everything reaches the backend as DecisionText.
func (j *Jobs) RateLocate(ctx context.Context, stage, question string, items []LocateItem) (map[string]float64, ScoreResult, error) {
	crit := locateFileCriterion
	if stage == "dirs" {
		crit = locateDirCriterion
	}
	si := make([]ScoreItem, len(items))
	for i, it := range items {
		si[i] = ScoreItem{ID: it.ID, Label: sanitize.ForDecision(it.Label, 320)}
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevExploreLocate) + "/" + stage,
		Criterion:   sanitize.ForDecision(crit, 0),
		Context:     sanitize.ForDecision(question, 1500),
		Items:       si,
		MaxRequests: 8,
	})
	return res.Scores, res, err
}

// VoiceTarget is something a spoken instruction may start: a skill, the
// security review, a crew, a saved process or prompt, an agent profile or a
// mode. Kind says which, and Name is how the user would say it.
type VoiceTarget struct {
	// ID is the caller's key for the target.
	ID string
	// Kind is skill, review, crew, process, prompt, agent or mode.
	Kind string
	// Name is the target's name; Description its one-line purpose, may be empty.
	Name, Description string
}

// voiceCriterion is the statement every target is judged against.
const voiceCriterion = "The speech in state.context is an explicit instruction to start exactly the thing named by the item, such as a command said to an assistant. A speech that only mentions the thing, asks about it, describes it or is about something else does not count."

// RateVoice rates how well a spoken instruction asks for each target. The
// scores are probabilities in [0,1]; a target the backend did not answer is
// absent from the map. The speech is user-spoken text and the names come from
// the user's own libraries; both reach the backend only as DecisionText.
func (j *Jobs) RateVoice(ctx context.Context, speech string, targets []VoiceTarget) (map[string]float64, ScoreResult, error) {
	items := make([]ScoreItem, 0, len(targets))
	for _, t := range targets {
		label := t.Kind + " " + t.Name
		if t.Description != "" {
			label += ": " + t.Description
		}
		items = append(items, ScoreItem{ID: sanitize.Ident(t.ID, 64), Label: sanitize.ForDecision(label, 240)})
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevVoiceCommand),
		Criterion:   sanitize.ForDecision(voiceCriterion, 0),
		Context:     sanitize.ForDecision(speech, 600),
		Items:       items,
		MaxRequests: 8,
	})
	return res.Scores, res, err
}

// PickVoice returns the one target a spoken instruction clearly asks for: its
// ID and score, or "" when no target, or more than one, is at or above the
// voice cut-off. More than one is ambiguous, so nothing runs.
func PickVoice(scores map[string]float64) (id string, score float64) {
	at := config.ActiveJevThresholds().VoiceAt
	n := 0
	for k, v := range scores {
		if v >= at {
			n++
			id, score = k, v
		}
	}
	if n != 1 {
		return "", 0
	}
	return id, score
}

// Scale is how big a request is: the deterministic ceremony of a goal turn
// (contract draft, changed-file prefetch, verification commands) is worth it
// for a staged request and only delay for a simple one.
type Scale string

const (
	// ScaleUnknown is the answer when the job is off, the backend did not
	// answer, or the scores are not conclusive. The ordinary behaviour runs.
	ScaleUnknown Scale = ""
	// ScaleSimple is a request of a few direct actions with nothing to
	// investigate or design.
	ScaleSimple Scale = "simple"
	// ScaleStaged is a request with dependent stages, investigation or design.
	ScaleStaged Scale = "staged"
)

// scaleCriterion is the statement each item is judged against.
const scaleCriterion = "The request in state.context matches the description in the item. A simple request is a few direct actions that can start at once and need no investigation, design or planning, such as committing, pushing, renaming, running a command or a small edit. A staged request has several dependent stages, needs investigation or design, or touches many files."

// scale item ids, the harness's own constants.
const (
	scaleItemSimple = "simple"
	scaleItemStaged = "staged"
)

// RateScale rates a request as simple and as staged. The request is the user's
// prompt and reaches the backend only as DecisionText. The scores are
// probabilities in [0,1]; an item the backend did not answer is absent.
func (j *Jobs) RateScale(ctx context.Context, request string) (map[string]float64, ScoreResult, error) {
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:       string(config.JevRequestScale),
		Criterion: sanitize.ForDecision(scaleCriterion, 0),
		Context:   sanitize.ForDecision(request, 1500),
		Items: []ScoreItem{
			{ID: scaleItemSimple, Label: sanitize.ForDecision("A simple request: a few direct actions, no investigation or design.", 240)},
			{ID: scaleItemStaged, Label: sanitize.ForDecision("A staged request: several dependent stages, investigation, design or many files.", 240)},
		},
		MaxRequests: 4,
	})
	return res.Scores, res, err
}

// PickScale turns the two scores into a verdict. A request is simple only when
// the backend rates it simple at or above simple_at and rated staged below
// keep_at; an unanswered item is unknown, never a low score, and the request
// is then worked as it always was. The verdict only removes ceremony, so it
// needs a clear majority and never approves anything.
func PickScale(scores map[string]float64) (Scale, float64) {
	th := config.ActiveJevThresholds()
	simple, okS := scores[scaleItemSimple]
	staged, okT := scores[scaleItemStaged]
	switch {
	case !okS || !okT:
		return ScaleUnknown, 0
	case simple >= th.SimpleAt && staged < th.KeepAt:
		return ScaleSimple, simple
	case staged >= th.KeepAt && simple < th.KeepAt:
		return ScaleStaged, staged
	}
	return ScaleUnknown, 0
}

// AgentDefaultID is the pick option that stands for "no profile": the model
// may always decline, so a poor match is never forced.
const AgentDefaultID = "default"

// agentPickQuestion is the shared question every agent profile is ranked by.
const agentPickQuestion = "Which option is the best fit to carry out the request in state.context? Choose default unless one profile clearly matches what the request asks for."

// PickAgent ranks the user's agent profiles for a request, with a final
// default option that means no profile. offers are the profiles, each with an
// identifier-safe ID the caller maps back to a name and a description; the
// request is the user's prompt. Both reach the backend only as DecisionText.
func (j *Jobs) PickAgent(ctx context.Context, request string, offers []ScoreItem) (PickResult, error) {
	opts := make([]ScoreItem, 0, len(offers)+1)
	opts = append(opts, offers...)
	opts = append(opts, ScoreItem{
		ID:    AgentDefaultID,
		Label: sanitize.ForDecision("default: a general coding request that none of the listed profiles clearly fits", 0),
	})
	return j.Client.Pick(ctx, PickRequest{
		Job:      string(config.JevAgentPick),
		Question: sanitize.ForDecision(agentPickQuestion, 0),
		Context:  sanitize.ForDecision("User request: "+request, 1500),
		Options:  opts,
	})
}
