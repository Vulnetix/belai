package vulnetixcli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Guardrail is one AI Firewall guardrail as `ai-firewall get -o json` and
// `ai-firewall policy guardrail … -o json` report it.
type Guardrail struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	RuleType string `json:"ruleType"`
	Action   string `json:"action"`
	Pattern  string `json:"pattern"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"`
}

// ErrPlanLocked reports that the org's plan does not include guardrails.
var ErrPlanLocked = errors.New("AI Firewall guardrails need the Teams plan")

// AiFirewallGuardrails reads the org's guardrails. It is read-only, so it runs
// with --disable-memory.
func (c CLI) AiFirewallGuardrails(ctx context.Context) ([]Guardrail, error) {
	res, err := c.Exec(ctx, "--disable-memory", "ai-firewall", "get", "-o", "json")
	if err != nil {
		return nil, cliFailure(res, err)
	}
	var out struct {
		Guardrails []Guardrail `json:"guardrails"`
		PlanLocked bool        `json:"planLocked"`
	}
	if err := json.Unmarshal([]byte(jsonBody(res.Stdout)), &out); err != nil {
		return nil, errors.New("ai-firewall get: unreadable reply")
	}
	return out.Guardrails, nil
}

// ApplyGuardrail runs one `ai-firewall policy guardrail …` argument list
// (rolemanager.FirewallGuardrailArgs) and returns the created rule's UUID
// when the reply names one. A reply that says the plan is locked is
// ErrPlanLocked.
func (c CLI) ApplyGuardrail(ctx context.Context, args []string) (string, error) {
	if len(args) < 4 || args[0] != "ai-firewall" || args[1] != "policy" || args[2] != "guardrail" {
		return "", errors.New("not a guardrail command")
	}
	res, err := c.Exec(ctx, append([]string{"--disable-memory"}, args...)...)
	if err != nil {
		return "", cliFailure(res, err)
	}
	var out struct {
		UUID       string     `json:"uuid"`
		Guardrail  *Guardrail `json:"guardrail"`
		PlanLocked bool       `json:"planLocked"`
	}
	if json.Unmarshal([]byte(jsonBody(res.Stdout)), &out) == nil {
		if out.PlanLocked {
			return "", ErrPlanLocked
		}
		if out.UUID != "" {
			return out.UUID, nil
		}
		if out.Guardrail != nil {
			return out.Guardrail.UUID, nil
		}
	}
	return "", nil
}

// jsonBody is the JSON object in a reply, skipping any line the CLI printed
// before it.
func jsonBody(s string) string {
	if i := strings.IndexByte(s, '{'); i >= 0 {
		return s[i:]
	}
	return s
}

// cliFailure turns a failed run into one short reason: the plan lock, a
// sign-in problem, or the CLI's own last line.
func cliFailure(res Result, err error) error {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "planlocked") || strings.Contains(low, "plan locked") || strings.Contains(low, "teams plan"):
		return ErrPlanLocked
	case strings.Contains(low, "no credentials") || strings.Contains(low, "unauthor") || strings.Contains(low, "not authenticated"):
		return errors.New("the Vulnetix CLI is not signed in")
	case res.TimedOut:
		return errors.New("the Vulnetix CLI timed out")
	}
	if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	if msg == "" {
		return err
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return errors.New(msg)
}
