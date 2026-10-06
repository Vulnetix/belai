package libstore

import (
	"encoding/json"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

func init() { register(libitem.Rewrite, rewriteStore{}) }

// rewriteStore keeps the Bash rewrite table where Belai reads it: `bash_rewrite`
// in the user's own settings.json (docs/bash-rewrite.md). There is one table, so
// the one item is always named bash_rewrite. The project layer may only switch the
// table off and is never read or written.
type rewriteStore struct{}

func hasRewrite(s config.Settings) bool {
	b := s.BashRewrite
	return b != nil && (len(b.Rules) > 0 || b.Enabled != nil)
}

func (rewriteStore) list() ([]Local, []Skipped, error) {
	s, err := config.LoadGlobal()
	if err != nil {
		return nil, nil, err
	}
	if !hasRewrite(s) {
		return nil, nil, nil
	}
	b, err := json.Marshal(libitem.RewriteDocument(s))
	if err != nil {
		return nil, nil, err
	}
	it, err := libitem.Validate(libitem.Rewrite, b)
	if err != nil {
		return nil, []Skipped{{Kind: libitem.Rewrite, Name: libitem.RewriteName, Reason: err.Error()}}, nil
	}
	return []Local{local(libitem.Rewrite, it.Name, it.Doc)}, nil, nil
}

func (rewriteStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	d, err := libitem.ParseRewrite(it.Doc)
	if err != nil {
		return Result{}, err
	}
	path, err := config.GlobalSettingsPath()
	if err != nil {
		return Result{}, err
	}
	replaced := false
	err = config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		if hasRewrite(*s) {
			if !o.Overwrite {
				return ErrExists
			}
			replaced = true
		}
		t := &config.BashRewriteSettings{Enabled: d.Enabled}
		if len(d.Rules) > 0 {
			t.Rules = d.Rules
		}
		s.BashRewrite = t
		if t.Enabled == nil && len(t.Rules) == 0 {
			s.BashRewrite = nil // an empty table says nothing
		}
		return validateWritten(*s)
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Replaced: replaced, Where: path + " (bash_rewrite)"}, nil
}
