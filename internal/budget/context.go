package budget

import "sort"

// TopToolResults is how many tool results a context breakdown lists.
const TopToolResults = 5

// ToolSize is one tool's share of a request: a harness tool identifier and its
// estimated tokens.
type ToolSize struct {
	Name   string
	Tokens int
}

// ContextBreakdown is where the tokens of one request went, against the model's
// context window. Every figure is an estimate of a size the harness already
// measured; no content is held.
type ContextBreakdown struct {
	System      int
	ToolDefs    int
	History     int
	ToolResults int
	// Used is the sum of the four parts above.
	Used int
	// Window is the model's context window in tokens, or 0 when unknown. With
	// no window, Percent and Free are 0 and HasWindow is false: a window is
	// never guessed.
	Window    int
	HasWindow bool
	// Percent is Used as a whole-number share of Window, clamped to 0..100.
	Percent int
	// Free is the room left in the window, never below 0.
	Free int
	// Top is the largest tool results, by size then name, at most
	// TopToolResults.
	Top []ToolSize
}

// Breakdown composes a context breakdown from the sizes of one request's parts.
// It takes plain integers so it does not depend on the run package. A nil
// toolResults map, a negative size and a zero or negative window are all
// handled: a negative size counts as zero and no window means no gauge.
func Breakdown(system, toolDefs, history int, toolResults map[string]int, window int) ContextBreakdown {
	b := ContextBreakdown{System: max(system, 0), ToolDefs: max(toolDefs, 0), History: max(history, 0)}
	for name, n := range toolResults {
		if n <= 0 {
			continue
		}
		b.ToolResults += n
		b.Top = append(b.Top, ToolSize{Name: name, Tokens: n})
	}
	sort.Slice(b.Top, func(i, j int) bool {
		if b.Top[i].Tokens != b.Top[j].Tokens {
			return b.Top[i].Tokens > b.Top[j].Tokens
		}
		return b.Top[i].Name < b.Top[j].Name
	})
	if len(b.Top) > TopToolResults {
		b.Top = b.Top[:TopToolResults]
	}
	b.Used = b.System + b.ToolDefs + b.History + b.ToolResults
	if window > 0 {
		b.Window = window
		b.HasWindow = true
		b.Percent = min(max(int(int64(b.Used)*100/int64(window)), 0), 100)
		b.Free = max(window-b.Used, 0)
	}
	return b
}
