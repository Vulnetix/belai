package agentprofile

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func ptr(b bool) *bool { return &b }

func TestProfileRoundTripsNewFields(t *testing.T) {
	p := AgentProfile{
		Name:          "test",
		Description:   "d",
		SystemPrompt:  "sp",
		Mode:          ModeSingle,
		Provider:      "llama-server",
		Model:         "default",
		Effort:        "low",
		Guardrails:    ptr(true),
		AskPermission: ptr(false),
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AgentProfile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Provider != "llama-server" || got.Model != "default" || got.Effort != "low" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Guardrails == nil || !*got.Guardrails {
		t.Fatal("guardrails lost")
	}
	if got.AskPermission == nil || *got.AskPermission {
		t.Fatal("ask_permission lost")
	}
}

func TestValidateRejectsBadEffort(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
		Effort:       "extreme",
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "effort") {
		t.Fatalf("expected effort error, got %v", err)
	}
}

func TestValidateRejectsInvalidProvider(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
		Provider:     "not-a-provider!",
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestValidateRejectsUnattendedUnguardedUnbounded(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeLoop,
		Autonomy:     AutonomyAutonomous,
		Guardrails:   ptr(false),
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "max_iterations") {
		t.Fatalf("expected max_iterations error, got %v", err)
	}
}

func TestValidateAllowsUnguardedWithMaxIterations(t *testing.T) {
	p := AgentProfile{
		Name:          "test",
		Description:   "d",
		SystemPrompt:  "sp",
		Mode:          ModeLoop,
		Autonomy:      AutonomyAutonomous,
		Guardrails:    ptr(false),
		MaxIterations: 10,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAllowsLoopWithoutGuardrailsDrop(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeLoop,
		Autonomy:     AutonomyAutonomous,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAcceptsEmptyProviderAndModel(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFileIsNotSerialised(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
		File:         "custom.json",
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "custom.json") {
		t.Fatalf("File leaked into JSON: %s", data)
	}
	if strings.Contains(string(data), "\"file\"") {
		t.Fatalf("file key leaked into JSON: %s", data)
	}
}

func TestFileNameUsesFileWhenSet(t *testing.T) {
	p := AgentProfile{Name: "bot name!", File: "custom.json"}
	if got := p.FileName(); got != "custom.json" {
		t.Fatalf("FileName = %q, want custom.json", got)
	}
	if got := deriveFileName("bot name!"); got != "bot_name.json" {
		t.Fatalf("deriveFileName = %q", got)
	}
}

func TestValidateFileName(t *testing.T) {
	valid := []string{"a.json", "triage-deps.json", "a.b_c-1.json"}
	for _, f := range valid {
		if err := ValidateFileName(f); err != nil {
			t.Fatalf("ValidateFileName(%q) = %v, want nil", f, err)
		}
	}
	invalid := map[string]string{
		"":          "file name is required",
		"no-ext":    "must end in .json",
		"../x.json": "path separators",
		"a/b.json":  "path separators",
		"a b.json":  "unsafe characters",
		".json":     "non-empty stem",
	}
	for f, want := range invalid {
		if err := ValidateFileName(f); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateFileName(%q) = %v, want containing %q", f, err, want)
		}
	}
}

func TestValidateRejectsBuiltinFileNameCollision(t *testing.T) {
	// Find a built-in and derive its on-disk filename, then pin it as File on a
	// user profile with a different name.
	names := builtinNames()
	if len(names) == 0 {
		t.Skip("no built-in profiles")
	}
	collision := builtinFileName(names[0])
	p := AgentProfile{
		Name:         "not-a-builtin",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
		File:         collision,
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "collides with a built-in") {
		t.Fatalf("Validate = %v, want built-in collision", err)
	}
}

func TestKnownToolsAndValidEffortsMatchMaps(t *testing.T) {
	tools := KnownTools()
	if len(tools) != len(knownToolNames) {
		t.Fatalf("KnownTools length = %d, want %d", len(tools), len(knownToolNames))
	}
	for _, name := range tools {
		if !knownToolNames[name] {
			t.Fatalf("KnownTools has %q not in knownToolNames", name)
		}
	}
	if !sort.StringsAreSorted(tools) {
		t.Fatalf("KnownTools not sorted: %v", tools)
	}

	efforts := ValidEfforts()
	if !reflect.DeepEqual(efforts, []string{"low", "medium", "high", "none"}) {
		t.Fatalf("ValidEfforts = %v", efforts)
	}
	for _, e := range efforts {
		if !validEfforts[e] {
			t.Fatalf("ValidEfforts has %q not in validEfforts", e)
		}
	}
	for e := range validEfforts {
		found := false
		for _, got := range efforts {
			if got == e {
				found = true
			}
		}
		if !found {
			t.Fatalf("ValidEfforts missing %q", e)
		}
	}
}

func TestStubPassesValidate(t *testing.T) {
	if err := Stub("stub-bot").Validate(); err != nil {
		t.Fatalf("Stub().Validate() = %v", err)
	}
}
