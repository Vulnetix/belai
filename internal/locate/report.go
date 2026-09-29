package locate

import (
	"fmt"
	"sort"
	"strings"
)

// DryRun describes what a search would look at, without ranking anything and
// without any request: how many eligible files and bytes each top-level
// directory holds, and what was left out and why.
func DryRun(inv *Inventory) string {
	if inv == nil {
		return "no inventory"
	}
	type agg struct {
		files int
		bytes int64
	}
	byTop := map[string]*agg{}
	var total agg
	for _, f := range inv.Files {
		top := "."
		if i := strings.IndexByte(f.Path, '/'); i >= 0 {
			top = f.Path[:i]
		}
		a := byTop[top]
		if a == nil {
			a = &agg{}
			byTop[top] = a
		}
		a.files++
		a.bytes += f.Size
		total.files++
		total.bytes += f.Size
	}
	tops := make([]string, 0, len(byTop))
	for k := range byTop {
		tops = append(tops, k)
	}
	sort.Strings(tops)
	var b strings.Builder
	fmt.Fprintf(&b, "%d eligible files, %s\n", total.files, humanSize(total.bytes))
	for _, k := range tops {
		fmt.Fprintf(&b, "  %s  %d files, %s\n", k, byTop[k].files, humanSize(byTop[k].bytes))
	}
	reasons := make([]string, 0, len(inv.Skipped))
	for k := range inv.Skipped {
		reasons = append(reasons, k)
	}
	sort.Strings(reasons)
	if len(reasons) > 0 {
		b.WriteString("left out:")
		for _, k := range reasons {
			fmt.Fprintf(&b, " %s %d,", k, inv.Skipped[k])
		}
		b.WriteString("\n")
	}
	if inv.Truncated {
		fmt.Fprintf(&b, "the walk stopped at %d files\n", MaxFiles)
	}
	return strings.TrimRight(strings.ReplaceAll(b.String(), ",\n", "\n"), "\n")
}
