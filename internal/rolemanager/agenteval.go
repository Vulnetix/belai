package rolemanager

import (
	"context"
	"errors"
	"fmt"
)

// AgentVerdict is the strict single-token output of the agent-loop evaluator.
type AgentVerdict string

const (
	// AgentContinue means keep looping immediately.
	AgentContinue AgentVerdict = "CONTINUE"
	// AgentPause means stop spending tokens and wait for the user.
	AgentPause AgentVerdict = "PAUSE"
	// AgentSleep means sleep one schedule interval, then keep looping.
	AgentSleep AgentVerdict = "SLEEP"
	// AgentStop means the loop's work is finished.
	AgentStop AgentVerdict = "STOP"
)

// ParseAgentVerdict maps a raw evaluator output to an AgentVerdict. It accepts
// a token that stands alone after normalizing away reasoning blocks and
// markdown wrappers, and rejects anything else. Malformed output fails closed
// to AgentPause — stop spending tokens, wait for the user.
func ParseAgentVerdict(raw string) (AgentVerdict, error) {
	s, err := matchSentinel(raw, []string{
		string(AgentContinue),
		string(AgentPause),
		string(AgentSleep),
		string(AgentStop),
	})
	if err != nil {
		return "", fmt.Errorf("malformed agent evaluator output %q: want a single verdict token", raw)
	}
	return AgentVerdict(s), nil
}

// agentEvalSystemPrompt instructs the loop evaluator to answer with exactly
// one verdict token and nothing else.
const agentEvalSystemPrompt = `You are a background-agent loop evaluator for an LLM coding harness. You are shown an agent's stated goals and its most recent output. Decide whether the agent should keep working, pause for the user, sleep one schedule interval, or stop, and reply with a single token and nothing else — no punctuation, no explanation, no surrounding text.

Reply with exactly one of these tokens:
- CONTINUE: the goals are not yet met and the agent should keep working now.
- PAUSE: the agent should stop and wait for the user before doing more.
- SLEEP: the agent should wait one schedule interval and then continue.
- STOP: the goals are met or the work is finished.`

// BuildAgentEvalPayload constructs the agent-loop evaluator request. Tools,
// Skills, and Agent are always empty: the evaluator turn must never expose
// tools, skills, or an agent block.
func BuildAgentEvalPayload(profileGoals, recentOutput string) ClassifierPayload {
	user := "Agent goals:\n" + profileGoals + "\n\nMost recent output:\n" + recentOutput
	return ClassifierPayload{
		System:                 agentEvalSystemPrompt,
		User:                   user,
		AllowReasoningFallback: true,
		UseCase:                UseCaseAgentEval,
	}
}

// ErrMalformedAgentEval reports that the agent-loop evaluator returned a
// non-sentinel reply. EvaluateAgent still returns AgentPause alongside this
// error so the fail-closed verdict survives, while a caller that wants to
// distinguish a broken evaluator from a genuine PAUSE can.
var ErrMalformedAgentEval = errors.New("malformed agent evaluator output")

// EvaluateAgent sends the agent's goals and recent output to the evaluator and
// parses the strict verdict. A transport error is returned as ("", err). A
// malformed reply fails closed to (AgentPause, ErrMalformedAgentEval): stop
// spending tokens, wait for the user.
func EvaluateAgent(ctx context.Context, c Classifier, profileGoals, recentOutput string) (AgentVerdict, error) {
	raw, err := c.Classify(ctx, BuildAgentEvalPayload(profileGoals, recentOutput))
	if err != nil {
		return "", err
	}
	s, err := ParseAgentVerdict(raw)
	if err != nil {
		record(EventAgentEval, string(AgentPause), "", "malformed: "+traceSnippet(raw), 0)
		return AgentPause, ErrMalformedAgentEval
	}
	record(EventAgentEval, string(s), "", "", 0)
	return s, nil
}
