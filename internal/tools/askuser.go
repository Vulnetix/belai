package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/clarify"
)

// AskUserQuestion lets the model ask the user multiple-choice questions
// instead of guessing. It is the trained Claude Code name and argument shape,
// offered in every mode. In agent and goal mode the harness asks at once and
// the answers are the tool result. In plan mode, final pass included, asking
// ends the planning pass and the answers start a new agent-mode turn with the
// full tool surface (see agent.runWithClarify). It performs no I/O itself.
type AskUserQuestion struct{}

// AskUserSentinel is the tool result the pass loop recognises. It is written
// so the conversation still reads correctly: the answers arrive as the next
// user turn.
const AskUserSentinel = "Questions sent to the user; waiting for their answers."

// Definition describes the tool to the model.
func (AskUserQuestion) Definition() Definition {
	return Definition{
		Name: "AskUserQuestion",
		Description: "Ask the user 1 to 6 multiple-choice questions when a decision is genuinely theirs " +
			"and you cannot settle it from the code or a sensible default. Each question has 2 to 4 options; " +
			"the user may also add a note. The answers come back as this tool's result and you continue with every tool. " +
			"In plan mode, asking ends planning and the answers start a new turn with every tool. " +
			"Do not ask a question you or the harness already asked this session, and do not ask when the prompt, " +
			"the conversation, the code or a sensible default already gives the best answer: use that answer and state it. " +
			"Do not use it to ask for approval of a plan; use ExitPlanMode for that.",
		Properties: map[string]Property{
			"questions": {
				Type:        "array",
				Description: "The questions, 1 to 6.",
				Items: &Property{
					Type: "object",
					Properties: map[string]Property{
						"question":    {Type: "string", Description: "One line, ending in '?' or '.'."},
						"header":      {Type: "string", Description: "Optional short label for the question."},
						"multiSelect": {Type: "boolean", Description: "Allow more than one option."},
						"options": {
							Type:        "array",
							Description: "2 to 4 options.",
							Items: &Property{
								Type: "object",
								Properties: map[string]Property{
									"label":       {Type: "string", Description: "Short option text."},
									"description": {Type: "string", Description: "Optional one-line explanation."},
								},
								Required: []string{"label"},
							},
						},
					},
					Required: []string{"question", "options"},
				},
			},
		},
		Required: []string{"questions"},
	}
}

// Kind is read-only so plan mode keeps it; its result is harness-composed.
func (AskUserQuestion) Kind() Kind { return KindRead }

// Subject has no permission subject.
func (AskUserQuestion) Subject(args map[string]any) string { return "" }

// Mutates reports false: asking performs no workspace I/O.
func (AskUserQuestion) Mutates() bool { return false }

// Execute validates the questions and returns the sentinel. An invalid
// questionnaire is a tool error, so the model can fix it in the same pass.
func (AskUserQuestion) Execute(ctx context.Context, args map[string]any) (Result, error) {
	if _, err := QuestionnaireFromArgs(args); err != nil {
		return Result{}, err
	}
	return Result{Content: AskUserSentinel}, nil
}

// QuestionnaireFromArgs converts AskUserQuestion arguments into a sanitized,
// validated clarify questionnaire.
func QuestionnaireFromArgs(args map[string]any) (clarify.Questionnaire, error) {
	raw, ok := args["questions"].([]any)
	if !ok || len(raw) == 0 {
		return clarify.Questionnaire{}, fmt.Errorf("questions must be a non-empty array")
	}
	var q clarify.Questionnaire
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return clarify.Questionnaire{}, fmt.Errorf("question %d must be an object", i+1)
		}
		text, _ := m["question"].(string)
		if h, _ := m["header"].(string); strings.TrimSpace(h) != "" && !strings.HasPrefix(strings.TrimSpace(text), strings.TrimSpace(h)) {
			text = strings.TrimSpace(h) + ": " + strings.TrimSpace(text)
		}
		multi, _ := m["multiSelect"].(bool)
		g := clarify.Group{Context: strings.TrimSpace(text), Multi: multi}
		opts, _ := m["options"].([]any)
		for _, o := range opts {
			om, ok := o.(map[string]any)
			if !ok {
				continue
			}
			label, _ := om["label"].(string)
			desc, _ := om["description"].(string)
			g.Options = append(g.Options, clarify.Option{Label: strings.TrimSpace(label), Description: strings.TrimSpace(desc)})
		}
		q.Groups = append(q.Groups, g)
	}
	q = q.Sanitized()
	if err := q.Validate(); err != nil {
		return clarify.Questionnaire{}, fmt.Errorf("invalid questions: %v", err)
	}
	return q, nil
}

var (
	_ Tool    = AskUserQuestion{}
	_ Mutator = AskUserQuestion{}
)
