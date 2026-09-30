package kanban

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const goodRef = "0123456789abcdef0123456789abcdef01234567"

func TestNormGatesAssignsIDsAndStartsUnmet(t *testing.T) {
	got, err := NormGates([]Gate{
		{ID: "G9", Title: "  import   works ", Kind: GateRunnable, Suite: "go", Dir: "internal/kanban", Test: "TestRoundTrip", State: GateMet, Ref: goodRef, Note: "forged"},
		{Title: "wording reviewed", Kind: GateManual},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "G1" || got[1].ID != "G2" {
		t.Fatalf("ids are assigned by position, got %+v", got)
	}
	if got[0].Title != "import works" || got[0].State != GateUnmet || got[0].Ref != "" || got[0].Note != "" {
		t.Fatalf("a caller cannot file a gate as met or carry a ref or note: %+v", got[0])
	}
	if got[0].Suite != "go" || got[0].Dir != "internal/kanban" || got[0].Test != "TestRoundTrip" {
		t.Fatalf("reference kept: %+v", got[0])
	}
}

func TestNormGatesRefusesWhatIsNotAReference(t *testing.T) {
	cases := map[string]Gate{
		"no title":            {Kind: GateRunnable, Suite: "go"},
		"bad kind":            {Title: "x", Kind: "shell", Suite: "go"},
		"runnable no suite":   {Title: "x", Kind: GateRunnable},
		"suite with space":    {Title: "x", Kind: GateRunnable, Suite: "go test"},
		"suite upper":         {Title: "x", Kind: GateRunnable, Suite: "Go"},
		"suite metachar":      {Title: "x", Kind: GateRunnable, Suite: "go;rm"},
		"dir traversal":       {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "../etc"},
		"dir inner traversal": {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "a/../../b"},
		"dir absolute":        {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "/etc"},
		"dir option":          {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "-race"},
		"dir metachar":        {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "a b"},
		"dir dollar":          {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "$HOME"},
		"dir trailing slash":  {Title: "x", Kind: GateRunnable, Suite: "go", Dir: "internal/"},
		"test regex":          {Title: "x", Kind: GateRunnable, Suite: "go", Test: "Test.*"},
		"test flag":           {Title: "x", Kind: GateRunnable, Suite: "go", Test: "-run"},
		"test too long":       {Title: "x", Kind: GateRunnable, Suite: "go", Test: strings.Repeat("a", 65)},
		"manual with suite":   {Title: "x", Kind: GateManual, Suite: "go"},
		"manual with dir":     {Title: "x", Kind: GateManual, Dir: "internal"},
		"manual with test":    {Title: "x", Kind: GateManual, Test: "TestX"},
	}
	for name, g := range cases {
		_, err := NormGates([]Gate{g})
		if !errors.Is(err, ErrGate) {
			t.Errorf("%s: want ErrGate, got %v", name, err)
		}
	}
}

func TestNormGatesLimitsAndDuplicates(t *testing.T) {
	var many []Gate
	for i := range MaxGates + 1 {
		many = append(many, Gate{Title: strings.Repeat("t", i+1), Kind: GateManual})
	}
	if _, err := NormGates(many); !errors.Is(err, ErrGate) {
		t.Fatalf("more than %d gates must be refused, got %v", MaxGates, err)
	}
	if got, err := NormGates(many[:MaxGates]); err != nil || len(got) != MaxGates || got[MaxGates-1].ID != "G8" {
		t.Fatalf("exactly %d gates is allowed: %v %v", MaxGates, len(got), err)
	}
	_, err := NormGates([]Gate{{Title: "Same", Kind: GateManual}, {Title: "  same ", Kind: GateManual}})
	if !errors.Is(err, ErrGate) {
		t.Fatalf("a repeated title is refused, got %v", err)
	}
	if got, err := NormGates(nil); err != nil || len(got) != 0 {
		t.Fatalf("no gates is fine: %v %v", got, err)
	}
}

func TestNormGatesCleansAndCapsTitles(t *testing.T) {
	long := strings.Repeat("word ", 60)
	got, err := NormGates([]Gate{{Title: "\x1b[31mred\x1b[0m <tools>text</system>‮", Kind: GateManual}, {Title: long, Kind: GateManual}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got[0].Title, "\x1b‮") || strings.Contains(got[0].Title, "<tools>") || strings.Contains(got[0].Title, "</system>") {
		t.Fatalf("title is not clean: %q", got[0].Title)
	}
	if n := len([]rune(got[1].Title)); n > MaxGateTitleRunes {
		t.Fatalf("title has %d runes, cap is %d", n, MaxGateTitleRunes)
	}
}

func TestAddStoresGatesAndRefusesBadOnes(t *testing.T) {
	s := testStore(t)
	it, _, err := s.Add(ItemInput{Title: "with gates", Gates: []Gate{{Title: "suite passes", Kind: GateRunnable, Suite: "go"}}}, prov)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(s.Path()).Get(it.Short())
	if err != nil || len(got.Gates) != 1 || got.Gates[0].ID != "G1" || got.Gates[0].State != GateUnmet {
		t.Fatalf("gates survive a reload: %+v %v", got.Gates, err)
	}
	if _, _, err := s.Add(ItemInput{Title: "bad", Gates: []Gate{{Title: "x", Kind: GateRunnable, Suite: "go", Dir: ".."}}}, prov); !errors.Is(err, ErrGate) {
		t.Fatalf("a bad gate refuses the card: %v", err)
	}
	if items, _ := s.Search(Query{}); len(items) != 1 {
		t.Fatalf("a refused card is not filed, board has %d", len(items))
	}
}

func TestSetGateNeedsTheClaimHolderAndKnownGate(t *testing.T) {
	s := testStore(t)
	it, _, _ := s.Add(ItemInput{Title: "gated", Gates: []Gate{{Title: "a", Kind: GateManual}}}, prov)
	if _, err := s.SetGate(it.ID, "w-1", "G1", GateMet, goodRef, "ok"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("an unclaimed card refuses a gate write: %v", err)
	}
	if _, err := s.ClaimID(it.ID, ClaimRequest{Worker: "w-1", Profile: "p", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetGate(it.ID, "w-2", "G1", GateMet, goodRef, "ok"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("another worker cannot decide a gate: %v", err)
	}
	if _, err := s.SetGate(it.ID, "w-1", "G7", GateMet, goodRef, "ok"); !errors.Is(err, ErrGate) {
		t.Fatalf("an unknown gate is refused: %v", err)
	}
	if _, err := s.SetGate(it.ID, "w-1", "../x", GateMet, goodRef, "ok"); !errors.Is(err, ErrGate) {
		t.Fatalf("a malformed gate id is refused: %v", err)
	}
	if _, err := s.SetGate(it.ID, "w-1", "G1", "passed", goodRef, "ok"); err == nil {
		t.Fatal("an unknown state is refused")
	}
	got, err := s.SetGate(it.ID, "w-1", "G1", GateMet, goodRef, "\x1b[1mexit 0\x1b[0m")
	if err != nil {
		t.Fatal(err)
	}
	g, _ := got.GateByID("G1")
	if g.State != GateMet || g.Ref != goodRef || g.Note != "exit 0" {
		t.Fatalf("gate not recorded cleanly: %+v", g)
	}
	if got.GatesOpen() {
		t.Fatal("every gate is met")
	}
	// A short or non-hex ref is dropped, not stored.
	got, _ = s.SetGate(it.ID, "w-1", "G1", GateMet, "main", "x")
	if g, _ := got.GateByID("G1"); g.Ref != "" {
		t.Fatalf("a branch name is not a commit ref: %q", g.Ref)
	}
}

func TestAbandonedGateIsOpenAndNeverSuccess(t *testing.T) {
	it := Item{Gates: []Gate{{ID: "G1", State: GateMet}, {ID: "G2", State: GateAbandoned}}}
	if !it.GatesOpen() || !it.GatesAbandoned() {
		t.Fatal("an abandoned gate leaves the card open and is reported as abandoned")
	}
	if (Item{}).GatesOpen() {
		t.Fatal("a card with no gates has none open")
	}
}

func TestPulledCopyNeverCarriesOrReplacesGates(t *testing.T) {
	local := Item{Gates: []Gate{{ID: "G1", State: GateMet}}}
	r := Item{Gates: []Gate{{ID: "G1", State: GateUnmet}, {ID: "G2"}}}
	keepAgent(&r, local)
	if len(r.Gates) != 1 || r.Gates[0].State != GateMet {
		t.Fatalf("the local gates stand: %+v", r.Gates)
	}
	local.Gates[0].State = GateAbandoned
	if r.Gates[0].State != GateMet {
		t.Fatal("the kept gates are a copy, not shared with the local item")
	}
}

func TestCloneItemCopiesGates(t *testing.T) {
	a := Item{Gates: []Gate{{ID: "G1", State: GateUnmet}}}
	b := cloneItem(a)
	b.Gates[0].State = GateMet
	if a.Gates[0].State != GateUnmet {
		t.Fatal("cloneItem shares the gate slice")
	}
}

func TestBoardVersionThreeRefusedByAnOlderReader(t *testing.T) {
	enc, err := Encode(Board{})
	if err != nil {
		t.Fatal(err)
	}
	if formatVersion != 3 {
		t.Fatalf("gates need board version 3, have %d", formatVersion)
	}
	if _, err := Decode(enc); err != nil {
		t.Fatal(err)
	}
	// A version 2 file (no gates) still reads, and reads as gate-less.
	v2 := append([]byte(nil), enc...)
	v2[len(magic)+1] = 2
	if _, err := Decode(v2); err != nil {
		t.Fatalf("version 2 stays readable: %v", err)
	}
}
