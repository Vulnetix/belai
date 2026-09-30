package tools

import (
	"encoding/json"
	"testing"
)

// coreDefinitionBudget caps the wire size of the core tool definitions the
// default registry advertises on every request. They ride in the prompt of
// every model call, so growth here is paid per turn; measured at about 13.7 KB
// (roughly 3.4k tokens) when this guard was added. Raise it deliberately, with
// a benchmark that shows the added description earns its tokens.
const coreDefinitionBudget = 16 << 10

func TestCoreToolDefinitionsStayWithinBudget(t *testing.T) {
	core := 0
	for _, d := range Default(t.TempDir(), false).Definitions() {
		if !IsCoreTool(d.Name) {
			continue
		}
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal %s: %v", d.Name, err)
		}
		core += len(b)
	}
	if core > coreDefinitionBudget {
		t.Fatalf("core tool definitions are %d bytes, budget %d: they are sent on every request", core, coreDefinitionBudget)
	}
}
