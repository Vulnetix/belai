package libstore

import (
	"encoding/json"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

func init() { register(libitem.Budget, budgetStore{}) }

// budgetStore keeps the token budgets where Belai reads them: `token_budgets` in
// the user's own settings.json, with the two footer settings that go with them
// (`ui.budget_cycle_seconds`, `ui.budget_warn`). The host has one configuration,
// so there is at most one local item, named by LocalName; installing a document
// replaces the whole configuration, which is why it needs the request's say-so.
// Budgets are global: a project settings file cannot set them and is never read
// or written.
type budgetStore struct{}

func hasBudgets(s config.Settings) bool {
	return len(s.TokenBudgets) > 0 || (s.UI != nil && (s.UI.BudgetCycleSeconds != nil || s.UI.BudgetWarn != nil))
}

func (budgetStore) list() ([]Local, []Skipped, error) {
	s, err := config.LoadGlobal()
	if err != nil {
		return nil, nil, err
	}
	if !hasBudgets(s) {
		return nil, nil, nil
	}
	name := LocalName(libitem.Budget)
	b, err := json.Marshal(libitem.BudgetDocument(name, s))
	if err != nil {
		return nil, nil, err
	}
	it, err := libitem.Validate(libitem.Budget, b)
	if err != nil {
		return nil, []Skipped{{Kind: libitem.Budget, Name: name, Reason: err.Error()}}, nil
	}
	return []Local{local(libitem.Budget, it.Name, it.Doc)}, nil, nil
}

func (budgetStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	d, err := libitem.ParseBudget(it.Doc)
	if err != nil {
		return Result{}, err
	}
	path, err := config.GlobalSettingsPath()
	if err != nil {
		return Result{}, err
	}
	replaced := false
	err = config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		if hasBudgets(*s) {
			if !o.Overwrite {
				return ErrExists
			}
			replaced = true
		}
		s.TokenBudgets = nil
		if len(d.Budgets) > 0 {
			s.TokenBudgets = d.Budgets
		}
		if d.CycleSeconds != nil || d.Warn != nil || s.UI != nil {
			if s.UI == nil {
				s.UI = &config.UISettings{}
			}
			s.UI.BudgetCycleSeconds, s.UI.BudgetWarn = d.CycleSeconds, d.Warn
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if err := setLocalName(libitem.Budget, d.Name); err != nil {
		return Result{Replaced: replaced, Where: path}, err
	}
	return Result{Replaced: replaced, Where: path + " (token_budgets)"}, nil
}
