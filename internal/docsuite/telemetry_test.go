package docsuite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/otel"
	"github.com/vulnetix/belai/internal/run"
)

// returnedStrings lists the string literals the function fn in file returns.
func returnedStrings(t *testing.T, file, fn string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if r, ok := n.(*ast.ReturnStmt); ok {
				for _, e := range r.Results {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil {
							seen[s] = true
						}
					}
				}
			}
			return true
		})
	}
	var out []string
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// TestTelemetryPageNamesOnlyAllowedAttributes keeps every belai.* attribute
// the page names on the export allowlist, so the page cannot promise a key the
// code would drop and the code cannot export one the page does not name.
func TestTelemetryPageNamesOnlyAllowedAttributes(t *testing.T) {
	doc := docparity.Read(t, "docs/telemetry.md")
	named := map[string]bool{}
	for _, m := range regexp.MustCompile("`(belai\\.[a-z_.]+)`").FindAllStringSubmatch(doc, -1) {
		named[m[1]] = true
	}
	for _, attr := range []string{
		otel.AttrMode, otel.AttrOutcome, otel.AttrPasses, otel.AttrProvider, otel.AttrModel, otel.AttrRole,
		otel.AttrEstimated, otel.AttrToolName, otel.AttrToolKind, otel.AttrDecision, otel.AttrVerdict,
		otel.AttrHookEvent, otel.AttrProjectKey, otel.AttrAgentProfile, otel.AttrStopReason,
	} {
		if !otel.Allowed(attr) {
			t.Errorf("%s is not on the allowlist", attr)
		}
		if !named[attr] {
			t.Errorf("docs/telemetry.md does not name the exported attribute %s", attr)
		}
	}
	// Spans and metrics are not attributes; every other name must be one the
	// code emits.
	root := docparity.Root(t)
	var src strings.Builder
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				b, _ := os.ReadFile(p)
				src.Write(b)
			}
			return nil
		})
	}
	for name := range named {
		if otel.Allowed(name) {
			continue
		}
		if !strings.Contains(src.String(), `"`+name+`"`) {
			t.Errorf("docs/telemetry.md names %s, which no code emits", name)
		}
	}
}

// TestTelemetryOutcomesMatchTheCode keeps the outcome and stop-reason lists in
// the tables equal to the values the code can report.
func TestTelemetryOutcomesMatchTheCode(t *testing.T) {
	doc := docparity.Read(t, "docs/telemetry.md")
	root := docparity.Root(t)
	file := filepath.Join(root, "internal", "agent", "telemetry.go")
	for fn, label := range map[string]string{"turnOutcome": "belai.turn", "toolOutcome": "belai.tool_call"} {
		row := regexp.MustCompile("(?m)^\\| `" + regexp.QuoteMeta(label) + "` \\|.*$").FindString(doc)
		if row == "" {
			t.Fatalf("no row for %s", label)
		}
		for _, v := range returnedStrings(t, file, fn) {
			if !strings.Contains(row, "`"+v+"`") {
				t.Errorf("the %s row does not list the outcome %q that %s can return", label, v, fn)
			}
		}
	}
	row := regexp.MustCompile("(?m)^\\| `belai.worker.items` \\|.*$").FindString(doc)
	for _, r := range []run.StopReason{run.StopComplete, run.StopStalled, run.StopWithheld, run.StopMaxPasses, run.StopEvaluator, run.StopCancelled, run.StopError, run.StopIncomplete, run.StopCoordinated} {
		if !strings.Contains(row, "`"+string(r)+"`") {
			t.Errorf("the belai.worker.items row does not list the stop reason %q", r)
		}
	}
}
