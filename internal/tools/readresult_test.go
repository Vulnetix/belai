package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/offload"
)

// ReadResult takes a reference, never a path: nothing in its schema can name
// a file, and its result classifies.
func TestReadResultIsPathFreeAndClassified(t *testing.T) {
	def := ReadResultTool{}.Definition()
	for name := range def.Properties {
		if strings.Contains(strings.ToLower(name), "path") || name == "file" || name == "dir" {
			t.Fatalf("ReadResult takes a path-like argument %q", name)
		}
	}
	if !(ReadResultTool{}).Kind().NeedsClassifier() {
		t.Fatal("ReadResult results must classify")
	}
	if Mutates(ReadResultTool{}) {
		t.Fatal("ReadResult must be read-only")
	}
	if !IsCoreTool(ReadResultName) {
		t.Fatal("ReadResult must be a core tool so the preview's instruction works without ToolSearch")
	}
}

func TestReadResultSlices(t *testing.T) {
	store := offload.NewStore()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "row %d\n", i)
	}
	b.WriteString("panic: boom\n")
	preview, ok := store.Offload("Bash", b.String(), 100, 50)
	if !ok || !strings.Contains(preview, `ref="r1"`) {
		t.Fatalf("offload: ok=%v %q", ok, preview)
	}
	tool := ReadResultTool{Store: store}
	ctx := context.Background()

	res, err := tool.Execute(ctx, map[string]any{"ref": "r1", "pattern": "panic", "context": float64(1)})
	if err != nil || res.Kind != KindOffload || !strings.Contains(res.Content, "501\tpanic: boom") || !strings.Contains(res.Content, "500\trow 500") {
		t.Fatalf("pattern slice: %v %+v", err, res)
	}
	res, err = tool.Execute(ctx, map[string]any{"ref": "r1", "offset": float64(250), "limit": float64(2)})
	if err != nil || !strings.HasPrefix(res.Content, "250\trow 250\n251\trow 251\n") {
		t.Fatalf("window slice: %v %q", err, res.Content)
	}
	if _, err := tool.Execute(ctx, map[string]any{"ref": "r9"}); err == nil {
		t.Fatal("unknown ref must fail")
	}
	if _, err := (ReadResultTool{Store: offload.NewStore()}).Execute(ctx, map[string]any{"ref": "r1"}); err == nil {
		t.Fatal("another session's store must not resolve r1")
	}
	if _, err := tool.Execute(ctx, map[string]any{"ref": "r1", "pattern": "("}); err == nil {
		t.Fatal("invalid pattern must fail")
	}
	if _, err := tool.Execute(ctx, map[string]any{}); err == nil {
		t.Fatal("missing ref must fail")
	}
}
