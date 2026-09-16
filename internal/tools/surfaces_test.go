package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestWriteEditReachAllThreeSurfaces pins that the new tools survive every
// wire conversion the agent performs: the OpenAI shape, the Anthropic shape,
// and (covered separately) the Workers AI shape, which reuses openAITools.
func TestWriteEditReachAllThreeSurfaces(t *testing.T) {
	reg := Default(t.TempDir(), false)

	for _, name := range []string{"Write", "Edit"} {
		tool, ok := reg.Find(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		d := tool.Definition()

		o := d.OpenAITool()
		b, err := json.Marshal(o)
		if err != nil {
			t.Fatalf("marshal OpenAITool: %v", err)
		}
		if !strings.Contains(string(b), `"name":"`+name+`"`) {
			t.Fatalf("OpenAI JSON missing %s: %s", name, b)
		}
		props, _ := o.Function.Parameters["properties"].(map[string]any)
		if len(props) == 0 {
			t.Fatalf("OpenAI %s has no properties", name)
		}

		a := d.AnthropicTool()
		if a.Name != name {
			t.Fatalf("Anthropic name = %q, want %q", a.Name, name)
		}
		if a.InputSchema == nil {
			t.Fatalf("Anthropic %s has no input schema", name)
		}
	}
}
