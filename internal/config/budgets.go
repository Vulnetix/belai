package config

import (
	"fmt"
	"strings"
	"time"
)

// Token budget scopes. A budget caps the tokens one provider+model may spend
// in a session, a local calendar day, or a local calendar month. See
// docs/token-budgets.md for the business rules these types carry.
const (
	BudgetScopeSession = "session"
	BudgetScopeDay     = "day"
	BudgetScopeMonth   = "month"
)

// BudgetScopes lists the scopes in display order.
var BudgetScopes = []string{BudgetScopeSession, BudgetScopeDay, BudgetScopeMonth}

// DefaultBudgetCycleSeconds is how long the footer shows one budget before
// cycling to the next; MinBudgetCycleSeconds is the floor a shorter setting is
// clamped to.
const (
	DefaultBudgetCycleSeconds = 10
	MinBudgetCycleSeconds     = 2
)

// TokenBudget is one budget: a token allowance for one provider+model in one
// scope. Budgets are global; a project settings file cannot set them.
type TokenBudget struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Scope    string `json:"scope"`
	Tokens   int64  `json:"tokens"`
}

// ModelKey is the provider/model identity usage is recorded under.
func ModelKey(provider, model string) string { return provider + "/" + model }

// Key returns the budget's provider/model identity.
func (b TokenBudget) Key() string { return ModelKey(b.Provider, b.Model) }

// ValidateTokenBudgets rejects an unknown scope, an empty provider or model, a
// non-positive allowance, and a second budget for the same provider, model and
// scope. An invalid budget fails the whole resolve, as an invalid provider
// profile does, rather than silently tracking against the wrong limit.
func ValidateTokenBudgets(s Settings) error {
	seen := map[string]bool{}
	for i, b := range s.TokenBudgets {
		if strings.TrimSpace(b.Provider) == "" || strings.TrimSpace(b.Model) == "" {
			return fmt.Errorf("token_budgets[%d]: provider and model are required", i)
		}
		switch b.Scope {
		case BudgetScopeSession, BudgetScopeDay, BudgetScopeMonth:
		default:
			return fmt.Errorf("token_budgets[%d]: scope %q is invalid (want session, day or month)", i, b.Scope)
		}
		if b.Tokens <= 0 {
			return fmt.Errorf("token_budgets[%d]: tokens must be positive, got %d", i, b.Tokens)
		}
		k := b.Key() + "@" + b.Scope
		if seen[k] {
			return fmt.Errorf("token_budgets[%d]: a %s budget for %s is already set", i, b.Scope, b.Key())
		}
		seen[k] = true
	}
	return nil
}

// BudgetsFor returns the budgets set for provider+model in scope order
// (session, day, month).
func (s Settings) BudgetsFor(provider, model string) []TokenBudget {
	var out []TokenBudget
	for _, scope := range BudgetScopes {
		for _, b := range s.TokenBudgets {
			if b.Provider == provider && b.Model == model && b.Scope == scope {
				out = append(out, b)
			}
		}
	}
	return out
}

// BudgetCycle is how long the footer shows each budget of the selected model
// before cycling: DefaultBudgetCycleSeconds when unset or non-positive, and
// never less than MinBudgetCycleSeconds.
func (s Settings) BudgetCycle() time.Duration {
	n := DefaultBudgetCycleSeconds
	if s.UI != nil && s.UI.BudgetCycleSeconds != nil && *s.UI.BudgetCycleSeconds > 0 {
		n = max(*s.UI.BudgetCycleSeconds, MinBudgetCycleSeconds)
	}
	return time.Duration(n) * time.Second
}

// BudgetWarnEnabled reports whether a system line is printed on each model
// call for the selected model while any of its budgets is amber or red.
// Default off.
func (s Settings) BudgetWarnEnabled() bool {
	return s.UI != nil && s.UI.BudgetWarn != nil && *s.UI.BudgetWarn
}
