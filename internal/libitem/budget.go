package libitem

import (
	"encoding/json"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Budget limits, shared with the website and the server.
const (
	MaxBudgets = 100
	// MaxBudgetTokens is the largest allowance a JSON number carries without loss
	// in a browser (2^53 - 1).
	MaxBudgetTokens = 1<<53 - 1
	MaxBudgetName   = 128
	// MaxBudgetCycle is the longest the footer may show one budget: a day.
	MaxBudgetCycle = 86400
)

var (
	budgetFields      = []string{"name", "budgets", "cycle_seconds", "warn"}
	budgetEntryFields = []string{"provider", "model", "scope", "tokens"}
)

func init() {
	register(Budget, validateBudget, func(c []byte) error {
		d, err := ParseBudget(c)
		if err != nil {
			return err
		}
		if err := config.ValidateTokenBudgets(config.Settings{TokenBudgets: d.Budgets}); err != nil {
			return refuse("%s", clip(err.Error(), 200))
		}
		return nil
	})
}

// BudgetDoc is a validated budget set: the token budgets of the `token_budgets`
// setting plus the two footer settings that go with them (`ui.budget_cycle_seconds`
// and `ui.budget_warn`). A key the document leaves out stays nil.
type BudgetDoc struct {
	Name         string               `json:"name"`
	Budgets      []config.TokenBudget `json:"budgets"`
	CycleSeconds *int                 `json:"cycle_seconds,omitempty"`
	Warn         *bool                `json:"warn,omitempty"`
}

// ParseBudget validates a canonical budget document and decodes it.
func ParseBudget(canonical []byte) (BudgetDoc, error) {
	if _, err := validateBudget(canonical); err != nil {
		return BudgetDoc{}, err
	}
	var d BudgetDoc
	if err := json.Unmarshal(canonical, &d); err != nil {
		return BudgetDoc{}, refuse("the document does not match the schema")
	}
	return d, nil
}

// BudgetDocument is the library document for the budgets in s, under name. A
// footer setting that is not set is left out, so a document installed from the
// library exports as the same bytes.
func BudgetDocument(name string, s config.Settings) map[string]any {
	budgets := make([]map[string]any, 0, len(s.TokenBudgets))
	for _, b := range s.TokenBudgets {
		budgets = append(budgets, map[string]any{"provider": b.Provider, "model": b.Model, "scope": b.Scope, "tokens": b.Tokens})
	}
	d := map[string]any{"name": name, "budgets": budgets}
	if s.UI != nil {
		if s.UI.BudgetCycleSeconds != nil {
			d["cycle_seconds"] = *s.UI.BudgetCycleSeconds
		}
		if s.UI.BudgetWarn != nil {
			d["warn"] = *s.UI.BudgetWarn
		}
	}
	return d
}

// validateBudget checks a canonical budget document:
//
//	{name, budgets: [{provider, model, scope, tokens}], cycle_seconds?, warn?}
//
// budgets is required and may be empty. A budget names a provider and a model (not
// empty after trimming, no control character), a scope of session, day or month,
// and a whole number of tokens above zero; no two budgets share a provider, model
// and scope. cycle_seconds is how long the footer shows each budget (zero means
// Belai's default of 10 and a value under 2 is raised to 2) and warn switches the
// per-call warning line on; warn is a boolean, as Belai reads it.
func validateBudget(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "budget", budgetFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(Budget, n) }, nameRule)
	if err != nil {
		return "", err
	}
	budgets, present, err := list(m, "budgets", "budget", MaxBudgets)
	if err != nil {
		return "", err
	}
	if !present {
		return "", refuse("budget.budgets is required")
	}
	seen := map[string]bool{}
	for i, v := range budgets {
		b, err := entry(v, "budget.budgets", i)
		if err != nil {
			return "", err
		}
		where := "budget.budgets[" + itoa(i) + "]"
		if err := onlyKeys(b, where, budgetEntryFields...); err != nil {
			return "", err
		}
		provider, err := budgetText(b, "provider", where)
		if err != nil {
			return "", err
		}
		model, err := budgetText(b, "model", where)
		if err != nil {
			return "", err
		}
		scope, _, err := str(b, "scope", where)
		if err != nil {
			return "", err
		}
		if scope != "session" && scope != "day" && scope != "month" {
			return "", refuse("%s.scope must be session, day or month", where)
		}
		if _, present, err := whole(b, "tokens", where, 1, MaxBudgetTokens, 0); err != nil || !present {
			if err == nil {
				err = refuse("%s.tokens is required", where)
			}
			return "", err
		}
		key := provider + "/" + model + "@" + scope
		if seen[key] {
			return "", refuse("%s: a %s budget for %s/%s is already set", where, scope, cleanForMessage(provider), cleanForMessage(model))
		}
		seen[key] = true
	}
	if _, _, err := whole(m, "cycle_seconds", "budget", 0, MaxBudgetCycle, 0); err != nil {
		return "", err
	}
	if _, err := boolean(m, "warn", "budget", false); err != nil {
		return "", err
	}
	return name, nil
}

// budgetText reads a budget's provider or model: a string that is not empty after
// trimming, at most MaxBudgetName bytes, with no control character.
func budgetText(b map[string]any, key, where string) (string, error) {
	s, present, err := str(b, key, where)
	if err != nil {
		return "", err
	}
	if !present || strings.TrimSpace(s) == "" {
		return "", refuse("%s.%s is required", where, key)
	}
	return s, checkStr(s, key, where, MaxBudgetName, false)
}
