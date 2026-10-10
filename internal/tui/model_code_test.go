package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestModelScreenCodeModeRows(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := newModelScreen(t, t.TempDir())
	a.modelState.routingScope = "project"

	// Default: smart, and the explicit rows are disabled.
	rows := a.codeRows()
	if len(rows) != 3 || rows[0].key != "use" || rows[0].value != codeUseSmart {
		t.Fatalf("default code rows = %+v", rows)
	}
	if !rows[1].disabled || !rows[2].disabled {
		t.Fatalf("provider/model rows must be disabled unless custom: %+v", rows)
	}

	// Cycling to fast stores the tier explicitly.
	_ = a.stageCode("use", "code mode = fast", func(c *config.CodeSettings) {
		c.Model = &config.CodeModel{Tier: codeUseFast}
	})
	if got := a.codeUse(); got != codeUseFast {
		t.Fatalf("codeUse = %q after fast, settings %+v", got, a.settings.Code)
	}

	// Selecting the use row on custom opens the provider list.
	a.settings.Code = &config.CodeSettings{Model: &config.CodeModel{Provider: "openai", Model: "gpt-x"}}
	rows = a.codeRows()
	if rows[0].value != codeUseCustom || rows[1].disabled || rows[2].disabled {
		t.Fatalf("custom code rows = %+v", rows)
	}
	if rows[2].value != "gpt-x" {
		t.Fatalf("model row = %q", rows[2].value)
	}
}
