package run

import (
	"encoding/json"
	"testing"
)

// TestPhaseModelIDs pins the default phase-1/phase-2 classifier model ids that
// the resolver surfaces as the "configure this model" hints.
func TestPhaseModelIDs(t *testing.T) {
	if Phase1ModelID() == "" || Phase2ModelID() == "" {
		t.Fatal("phase model ids must be non-empty")
	}
	if Phase1ModelID() != "GuardrailsAI/prompt-saturation-attack-detector" {
		t.Fatalf("Phase1ModelID() = %q", Phase1ModelID())
	}
}

// TestOllamaBaseURL pins the environment-derived Ollama base URL seam,
// including scheme-less host:port and trailing-slash full URLs.
func TestOllamaBaseURL(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	if got := ollamaBaseURL(); got != "http://localhost:11434/v1" {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("OLLAMA_HOST", "10.0.0.1:11435")
	if got := ollamaBaseURL(); got != "http://10.0.0.1:11435/v1" {
		t.Fatalf("host:port = %q", got)
	}
	t.Setenv("OLLAMA_HOST", "https://ollama.example/")
	if got := ollamaBaseURL(); got != "https://ollama.example/v1" {
		t.Fatalf("full url = %q", got)
	}
}

// TestBuildOllamaAndLlamaBaseURL pins the decomposed-URL constructors and
// their per-server default ports.
func TestBuildOllamaAndLlamaBaseURL(t *testing.T) {
	if got := buildOllamaBaseURL("", "", ""); got != "http://localhost:11434/v1" {
		t.Fatalf("buildOllamaBaseURL default = %q", got)
	}
	if got := buildOllamaBaseURL("h", "1234", "https"); got != "https://h:1234/v1" {
		t.Fatalf("buildOllamaBaseURL decomposed = %q", got)
	}
	if got := buildLlamaBaseURL("", "", ""); got != "http://localhost:8080/v1" {
		t.Fatalf("buildLlamaBaseURL default = %q", got)
	}
	if got := buildLlamaBaseURL("h", "9", "https"); got != "https://h:9/v1" {
		t.Fatalf("buildLlamaBaseURL decomposed = %q", got)
	}
}

// TestParseToolCallArgs pins the dual JSON form: a JSON-encoded string of
// arguments (classic OpenAI) and a raw JSON object, plus the empty and
// malformed cases.
func TestParseToolCallArgs(t *testing.T) {
	args, err := parseToolCallArgs(nil)
	if err != nil || len(args) != 0 {
		t.Fatalf("empty = (%v, %v), want empty map", args, err)
	}

	args, err = parseToolCallArgs(json.RawMessage(`"{\"q\":\"x\"}"`))
	if err != nil || args["q"] != "x" {
		t.Fatalf("string form = (%v, %v)", args, err)
	}

	args, err = parseToolCallArgs(json.RawMessage(`{"n":2}`))
	if err != nil || args["n"] != float64(2) {
		t.Fatalf("object form = (%v, %v)", args, err)
	}

	if _, err := parseToolCallArgs(json.RawMessage(`"not-json"`)); err == nil {
		t.Fatal("malformed string form should error")
	}
	if _, err := parseToolCallArgs(json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed object form should error")
	}
}
