package kanban

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/session"
)

// Acceptance gates. A gate is one observable outcome a card must show before
// it is done. It is a reference, never a command: a runnable gate names one of
// the test suites the harness detected in the repository (internal/testdetect)
// and, for a Go suite, may narrow it to a package directory and one test name.
// The harness builds the argv from that table, so no model writes a command,
// and it decides the gate by the run's exit code alone. A manual gate has no
// such reference; a reviewer records its state.
//
// The harness sets a gate's id (G1, G2, ... by position), state, ref and note.
// No model argument reaches them, and a pulled copy of a card never carries
// or overwrites them (they stay on the host that verified them).

// GateKind says how a gate is decided.
type GateKind string

// The gate kinds.
const (
	// GateRunnable is decided by the harness running a detected suite.
	GateRunnable GateKind = "runnable"
	// GateManual is decided by a reviewer's recorded verdict.
	GateManual GateKind = "manual"
)

// GateState is where a gate stands.
type GateState string

// The gate states. Abandoned is terminal and is never success: a card with an
// abandoned gate is a handoff to a person, not a finished card.
const (
	GateUnmet     GateState = "unmet"
	GateMet       GateState = "met"
	GateAbandoned GateState = "abandoned"
)

// Limits.
const (
	// MaxGates is the most gates one card carries.
	MaxGates = 8
	// MaxGateTitleRunes caps a gate's title.
	MaxGateTitleRunes = 120
	// MaxGateNoteRunes caps a gate's harness-composed note.
	MaxGateNoteRunes = 200
)

// Gate is one acceptance gate of a card.
type Gate struct {
	// ID is G1, G2, ... by position, assigned by the harness.
	ID    string
	Title string
	Kind  GateKind
	// Suite is the detected suite a runnable gate runs ("go", "pytest",
	// "just-check"). Dir and Test narrow a Go suite to one package directory
	// (relative to the repository root) and one test name; both may be empty.
	Suite string
	Dir   string
	Test  string
	// State is unmet until the harness (runnable) or a reviewer (manual)
	// decides otherwise. Ref is the commit the state was decided at and Note a
	// one-line harness fact (exit status, counts).
	State GateState
	Ref   string
	Note  string
}

// Valid reports whether k is a gate kind.
func (k GateKind) Valid() bool { return k == GateRunnable || k == GateManual }

// Valid reports whether s is a gate state.
func (s GateState) Valid() bool {
	return s == GateUnmet || s == GateMet || s == GateAbandoned
}

var (
	gateSuiteRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	gateDirRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+/-]{0,127}$`)
	gateTestRE  = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	gateIDRE    = regexp.MustCompile(`^G[1-9]$`)
)

// ErrGate marks a gate that failed validation. The message names what to fix.
var ErrGate = errors.New("kanban: invalid acceptance gate")

func gateErr(i int, format string, args ...any) error {
	return fmt.Errorf("%w: gate %d %s", ErrGate, i+1, fmt.Sprintf(format, args...))
}

// NormGates validates and normalises the gates a card is filed with. Ids are
// assigned by position and state starts unmet whatever the input said, so a
// caller cannot file a gate as already met. An invalid gate is an error, never
// dropped: the caller tells the model what to fix.
func NormGates(in []Gate) ([]Gate, error) {
	if len(in) > MaxGates {
		return nil, fmt.Errorf("%w: at most %d gates a card", ErrGate, MaxGates)
	}
	out := make([]Gate, 0, len(in))
	seen := map[string]bool{}
	for i, g := range in {
		title := CleanTitle(g.Title)
		if utf8.RuneCountInString(title) > MaxGateTitleRunes {
			r := []rune(title)
			title = strings.TrimSpace(string(r[:MaxGateTitleRunes-1])) + "…"
		}
		if title == "" {
			return nil, gateErr(i, "needs a title that states the outcome")
		}
		if !g.Kind.Valid() {
			return nil, gateErr(i, "kind must be %s or %s", GateRunnable, GateManual)
		}
		n := Gate{ID: fmt.Sprintf("G%d", i+1), Title: title, Kind: g.Kind, State: GateUnmet}
		switch g.Kind {
		case GateRunnable:
			if !gateSuiteRE.MatchString(g.Suite) {
				return nil, gateErr(i, "runnable needs the name of a detected test suite")
			}
			n.Suite = g.Suite
			if g.Dir != "" {
				if !gateDirRE.MatchString(g.Dir) || slices.Contains(strings.Split(g.Dir, "/"), "..") || strings.HasSuffix(g.Dir, "/") {
					return nil, gateErr(i, "dir must be a relative package directory of letters, digits and . _ - / @ +")
				}
				n.Dir = g.Dir
			}
			if g.Test != "" {
				if !gateTestRE.MatchString(g.Test) {
					return nil, gateErr(i, "test must be one test name of letters, digits and underscores")
				}
				n.Test = g.Test
			}
		case GateManual:
			if g.Suite != "" || g.Dir != "" || g.Test != "" {
				return nil, gateErr(i, "is manual, so it names no suite, dir or test")
			}
		}
		key := strings.ToLower(n.Title)
		if seen[key] {
			return nil, gateErr(i, "repeats an earlier gate's title")
		}
		seen[key] = true
		out = append(out, n)
	}
	return out, nil
}

// GateByID returns the gate with the id, and whether the item has it.
func (it Item) GateByID(id string) (Gate, bool) {
	for _, g := range it.Gates {
		if g.ID == id {
			return g, true
		}
	}
	return Gate{}, false
}

// GatesOpen reports whether any gate is not met. An abandoned gate is open: it
// is never success.
func (it Item) GatesOpen() bool {
	for _, g := range it.Gates {
		if g.State != GateMet {
			return true
		}
	}
	return false
}

// GatesAbandoned reports whether any gate was abandoned.
func (it Item) GatesAbandoned() bool {
	for _, g := range it.Gates {
		if g.State == GateAbandoned {
			return true
		}
	}
	return false
}

func cloneGates(g []Gate) []Gate { return slices.Clone(g) }

// SetGate records the state of one gate of a card. Only the harness calls it
// (a fleet worker's verification or a reviewer's KanbanGate through the tool's
// checks): holder must be the worker holding the card's claim. The note is
// cleaned and capped, and ref is the commit the state was decided at.
func (s *Store) SetGate(ref, holder, gateID string, state GateState, gitRef, note string) (Item, error) {
	if !state.Valid() {
		return Item{}, fmt.Errorf("kanban: %q is not a gate state", state)
	}
	if !gateIDRE.MatchString(gateID) {
		return Item{}, fmt.Errorf("%w: %q is not a gate id", ErrGate, gateID)
	}
	var out Item
	err := s.mutate(true, func(b *Board) error {
		i, err := find(b, ref)
		if err != nil {
			return err
		}
		it := &b.Items[i]
		if holder == "" || it.ClaimedBy != holder {
			return ErrLeaseLost
		}
		j := slices.IndexFunc(it.Gates, func(g Gate) bool { return g.ID == gateID })
		if j < 0 {
			return fmt.Errorf("%w: the card has no gate %s", ErrGate, gateID)
		}
		g := &it.Gates[j]
		g.State = state
		g.Ref = CleanRef(gitRef)
		g.Note = cleanGateNote(note)
		it.Updated, it.Dirty = s.nowMs(), true
		appendHistory(it, Move{ID: session.MustID(), From: it.List, To: it.List, At: it.Updated, Note: fmt.Sprintf("gate %s %s", gateID, state)})
		out = cloneItem(*it)
		return nil
	})
	return out, err
}

func cleanGateNote(s string) string {
	s = CleanTitle(s)
	if utf8.RuneCountInString(s) > MaxGateNoteRunes {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:MaxGateNoteRunes-1])) + "…"
	}
	return s
}
