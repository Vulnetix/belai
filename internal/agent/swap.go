package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/shellsafe"
	"github.com/vulnetix/belai/internal/tools"
)

// bashSwap is a Bash call that the harness will run as one builtin tool
// instead. The model's call, with its id and name, is untouched: the tool
// result turn is still keyed on the Bash call, and only the executed call is
// replaced.
type bashSwap struct {
	name     string
	args     map[string]any
	tool     tools.Tool
	decision permissions.Decision
	scorePct int
}

// swapNote is the harness-composed line that opens the tool result, so the
// model knows its call was run differently. It names tools and a percentage
// and nothing the model or the fast model wrote.
func (sw *bashSwap) note() string {
	return fmt.Sprintf("[harness: your Bash call was replaced by %s (Jev rated it %d%% equivalent); the Bash command was not run; the output below is from %s]\n",
		sanitize.Ident(sw.name, 64), sw.scorePct, sanitize.Ident(sw.name, 64))
}

type swapCtxKey struct{}

// swappedFromBash reports that the call being executed replaced a Bash call.
func swappedFromBash(ctx context.Context) bool {
	v, _ := ctx.Value(swapCtxKey{}).(bool)
	return v
}

// withSwapped marks a context so promoteResult classifies the result as it
// would have classified Bash output, whatever the executed tool's own kind.
func withSwapped(ctx context.Context) context.Context {
	return context.WithValue(ctx, swapCtxKey{}, true)
}

// commandHash keys the set of commands already swapped once.
func commandHash(cmd string) string {
	h := sha256.Sum256([]byte(cmd))
	return hex.EncodeToString(h[:8])
}

// trySwap decides whether a Bash call is run as a builtin tool instead. It
// returns nil, and the call runs as Bash, unless every step succeeds:
//
//  1. the job is on, a decision backend exists and the command is one plain
//     command (no chaining, redirects or expansion);
//  2. the Bash call itself is not denied by a rule (a swap must not launder a
//     denied command);
//  3. Jev rates exactly one shortlisted builtin at or above the swap
//     threshold as a full replacement;
//  4. the fast model, asked to reconsider, writes arguments for that tool;
//  5. the arguments pass the tool's schema and every value in them comes from
//     the command (the fast model cannot invent a target);
//  6. the builtin is allowed on this surface and in this mode and is not
//     denied.
//
// A command the model sends again after a swap runs as Bash: one swap per
// command per session.
func (s *Session) trySwap(ctx context.Context, pipe *rolemanager.Pipeline, args map[string]any) *bashSwap {
	if s.jev == nil || !s.jev.Enabled(config.JevBashSwap) || pipe == nil || pipe.Classifier == nil {
		return nil
	}
	cmd, _ := args["command"].(string)
	an := shellsafe.Analyze(cmd)
	if !an.Simple() {
		return nil
	}
	if dec, _, _ := s.decidePermission("Bash", cmd); dec == permissions.DecisionBlock {
		return nil
	}
	key := commandHash(cmd)
	if s.swapped[key] {
		return nil
	}

	var cands []jev.SwapCandidate
	for _, name := range tools.SwapCandidates(an.Commands[0].Argv) {
		tool, ok := s.findCallable(name)
		if !ok {
			continue
		}
		if _, refusal := s.execTool(name); refusal != "" {
			continue
		}
		cands = append(cands, jev.SwapCandidate{Name: name, Description: firstSentence(tool.Definition().Description)})
	}
	if len(cands) == 0 {
		return nil
	}

	start := time.Now()
	scores, identity, err := s.jev.RateSwap(ctx, cmd, cands)
	if err != nil {
		return nil
	}
	winner, pct, ok := uniqueWinner(scores)
	if !ok {
		rolemanager.RecordBashSwap("kept", "", best(scores), identity, time.Since(start))
		return nil
	}

	tool, _ := s.findCallable(winner)
	def := tool.Definition()
	replan, keep, err := rolemanager.DecideBashReplan(ctx, pipe.Classifier, cmd, winner, schemaFor(def))
	if err != nil || keep {
		rolemanager.RecordBashSwap("kept", winner, pct, identity, time.Since(start))
		return nil
	}
	if !replanAcceptable(def, cmd, replan) {
		rolemanager.RecordBashSwap("refused", winner, pct, identity, time.Since(start))
		return nil
	}
	if !modes.ToolAllowed(winner, replan, s.planMode, s.planSurface) {
		rolemanager.RecordBashSwap("refused", winner, pct, identity, time.Since(start))
		return nil
	}
	dec, _, _ := s.decidePermission(winner, tool.Subject(replan))
	if dec == permissions.DecisionBlock {
		rolemanager.RecordBashSwap("refused", winner, pct, identity, time.Since(start))
		return nil
	}
	s.swapped[key] = true
	rolemanager.RecordBashSwap("swapped", winner, pct, identity, time.Since(start))
	return &bashSwap{name: winner, args: replan, tool: tool, decision: dec, scorePct: pct}
}

// uniqueWinner returns the single candidate rated at or above the swap
// threshold. Two or more, or none, is no winner: the harness only swaps when
// one builtin is clearly the right one.
func uniqueWinner(scores map[string]float64) (name string, pct int, ok bool) {
	names := make([]string, 0, len(scores))
	for n := range scores {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if scores[n] >= jev.SwapAt {
			if name != "" {
				return "", 0, false
			}
			name, pct = n, int(scores[n]*100+0.5)
		}
	}
	return name, pct, name != ""
}

// best is the highest score as a whole percent, for records of a kept call.
func best(scores map[string]float64) int {
	top := 0.0
	for _, s := range scores {
		if s > top {
			top = s
		}
	}
	return int(top*100 + 0.5)
}

// firstSentence is the opening sentence of a tool description, bounded.
func firstSentence(desc string) string {
	if i := strings.Index(desc, ". "); i > 0 {
		desc = desc[:i+1]
	}
	return sanitize.Line(desc, 200)
}

// schemaFor renders a tool's arguments compactly for the replan prompt.
func schemaFor(def tools.Definition) string {
	type prop struct {
		Type string   `json:"type"`
		Enum []string `json:"enum,omitempty"`
	}
	props := map[string]prop{}
	for k, p := range def.Properties {
		props[k] = prop{Type: p.Type, Enum: p.Enum}
	}
	b, _ := json.Marshal(map[string]any{"properties": props, "required": def.Required})
	return string(b)
}

// replanAcceptable applies the deterministic checks to arguments the fast
// model wrote: they must satisfy the tool's schema (undeclared keys, types,
// enums, formats) and be grounded in the command. A required argument may not
// be missing either, since the tool would fail on it.
func replanAcceptable(def tools.Definition, command string, args map[string]any) bool {
	if len(args) == 0 || tools.CheckArgs(def, args) != nil {
		return false
	}
	for _, req := range def.Required {
		if _, ok := args[req]; !ok {
			return false
		}
	}
	return grounded(def, command, args)
}

// grounded reports that every string and number in args comes from the
// command: it appears in it, is the current directory, or is one of the
// tool's enum values. Booleans carry no data and always pass. This is what
// stops the fast model choosing its own target.
func grounded(def tools.Definition, command string, args map[string]any) bool {
	for key, v := range args {
		switch x := v.(type) {
		case bool:
		case string:
			if x == "" || x == "." || enumHas(def.Properties[key].Enum, x) {
				continue
			}
			if !strings.Contains(command, x) {
				return false
			}
		case float64:
			if !strings.Contains(command, fmt.Sprintf("%d", int64(x))) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func enumHas(enum []string, s string) bool {
	for _, e := range enum {
		if e == s {
			return true
		}
	}
	return false
}
