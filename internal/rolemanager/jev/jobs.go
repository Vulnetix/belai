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
