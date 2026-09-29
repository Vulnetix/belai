package testpass

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// NewGate builds the Gate every surface uses for process output. The text is
// sanitised (delimiter markup removed) and then classified as tools.KindProcess
// unless the effective posture ignores unsafe tool results. The level is
// checked before the classifier is called, never after: a verdict that cannot
// change the outcome would only send the output to a provider for nothing.
// A classifier error or a non-proceed verdict withholds the text.
func NewGate(cfg run.Config, client *http.Client, cache *rolemanager.Cache, pol posture.Policy) Gate {
	return func(ctx context.Context, output string) (string, bool) {
		if pol.Level(posture.ToolResultUnsafe) == posture.Ignore {
			return sanitize.Sanitize(output), true
		}
		dec, err := run.NewPipeline(cfg, client, cache).Process(ctx, tools.Result{Kind: tools.KindProcess, Content: output})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			return "", false
		}
		return dec.Content, true
	}
}

// FixPrompt is the harness-authored prompt for a fail-branch turn. It is a
// constant the harness composed from facts (suite names, statuses, exit codes
// and the commands themselves): it holds no test output, which rides only as
// an attachment.
func FixPrompt(req FixRequest) string {
	var b strings.Builder
	b.WriteString("The post-end test pass failed.\n")
	for _, line := range strings.Split(req.Facts, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			b.WriteString("- " + sanitize.Line(line, 300) + "\n")
		}
	}
	switch {
	case req.OutputWithheld:
		b.WriteString("The failing output was withheld by the security classifier, so run the failing command yourself to see it.\n")
	case req.Output != "":
		b.WriteString("The failing output is attached.\n")
	}
	if req.Mode == "diagnose" {
		b.WriteString("Diagnose the root cause and propose the smallest fix. Do not edit any file.")
	} else {
		fmt.Fprintf(&b, "Find the root cause and fix it in the code under test, then re-run the failing command to confirm (pass %d of %d). "+
			"Do not delete, skip or weaken a test to make it pass unless the test itself is wrong, and say so if it is.", req.Pass, req.MaxPasses)
	}
	return b.String()
}

// FixAttachments returns the attachments for a fail-branch turn: the gated
// output as a process attachment, or none when it was withheld or empty.
func FixAttachments(req FixRequest) []run.Attachment {
	if req.Output == "" {
		return nil
	}
	return []run.Attachment{{Kind: "process", Label: "test output", Body: req.Output}}
}

// FixInput builds the agent turn for a request: goal mode with no contract
// draft for a fix (the harness constant is the objective), and the read-only
// plan surface for a diagnosis.
func FixInput(req FixRequest) agent.TurnInput {
	prompt := FixPrompt(req)
	in := agent.TurnInput{
		Prompt:        prompt,
		HarnessPrompt: prompt,
		Attachments:   FixAttachments(req),
		NoGoalDraft:   true,
	}
	if req.Mode == "diagnose" {
		in.ForceMode = modes.ModePlan
	} else {
		in.ForceMode = modes.ModeGoal
	}
	return in
}

// SessionFixer returns a Fixer that runs each request as one turn on sess,
// the blocking loop headless and ACP use. Every gate, permission rule and
// sandbox of the session applies to the loop's tool calls.
func SessionFixer(sess *agent.Session, observe func(agent.Event)) Fixer {
	return func(ctx context.Context, req FixRequest) error {
		_, err := sess.RunInputObserved(ctx, nil, FixInput(req), observe)
		return err
	}
}
