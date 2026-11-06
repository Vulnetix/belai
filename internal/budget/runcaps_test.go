package budget

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/transcript"
)

func capsHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BELAI_HOME", dir)
	return dir
}

// R1: with no cap set there is no meter, and the pass ceiling alone does not
// make one.
func TestRunCapsRule1_OffByDefault(t *testing.T) {
	capsHome(t)
	m, err := MeterFor(config.Settings{}, "s1")
	if err != nil || m != nil {
		t.Fatalf("meter=%v err=%v", m, err)
	}
	passes := config.Settings{Resilience: &config.ResilienceSettings{MaxPasses: 5}}
	if m, err := MeterFor(passes, "s1"); err != nil || m != nil {
		t.Fatalf("max_passes alone made a meter: %v %v", m, err)
	}
	three := 3000
	on := config.Settings{RunCaps: &config.RunCapsSettings{MaxTokens: &three}, Resilience: passes.Resilience}
	m, err = MeterFor(on, "s1")
	if err != nil || m == nil {
		t.Fatalf("meter=%v err=%v", m, err)
	}
	c := m.Caps()
	if c.Tokens != 3000 || c.Passes != 5 || c.FinalizeAt != 0.9 || c.MaxExtends != 3 || c.CostMicroUSD != 0 || c.Wall != 0 {
		t.Fatalf("caps = %+v", c)
	}
}

// R2: a finalise is reported once per reason at finalize_at, a hit on every
// check, and a hit outranks a finalise.
func TestRunCapsRule2_FinaliseOnceThenHit(t *testing.T) {
	m := NewMeter(Caps{Tokens: 1000, CostMicroUSD: 10_000_000})
	if v := m.Spend(0, 899); v.Level != OK {
		t.Fatalf("below finalize_at: %+v", v)
	}
	if v := m.Spend(0, 1); v.Level != Finalise || v.Reason != ReasonTokens {
		t.Fatalf("at finalize_at: %+v", v)
	}
	if v := m.Check(); v.Level != OK {
		t.Fatalf("finalise reported twice: %+v", v)
	}
	// A second dimension has its own once-latch.
	if v := m.Spend(9_000_000, 0); v.Level != Finalise || v.Reason != ReasonCost {
		t.Fatalf("cost finalise: %+v", v)
	}
	if v := m.Spend(0, 100); v.Level != Hit || v.Reason != ReasonTokens || !v.Warned {
		t.Fatalf("hit: %+v", v)
	}
	for i := 0; i < 3; i++ {
		if v := m.Check(); v.Level != Hit {
			t.Fatalf("a hit is reported on every check: %+v", v)
		}
	}
}

// R2: a hit with no earlier warning says so.
func TestRunCapsRule2_UnwarnedHit(t *testing.T) {
	m := NewMeter(Caps{Tokens: 1000})
	if v := m.Spend(0, 1500); v.Level != Hit || v.Warned {
		t.Fatalf("%+v", v)
	}
}

// R6: an extension adds half of the original ceiling, at most max_extends
// times, and the run is told to finalise again at the new ceiling.
func TestRunCapsRule6_ExtendStepAndLimit(t *testing.T) {
	m := NewMeter(Caps{Tokens: 1000, CostMicroUSD: 1_000_000, Wall: time.Hour, Passes: 10, MaxExtends: 2})
	m.Spend(0, 1000)
	if m.Check().Level != Hit {
		t.Fatal("expected a hit")
	}
	if !m.Extend() {
		t.Fatal("first extension refused")
	}
	c := m.Caps()
	if c.Tokens != 1500 || c.CostMicroUSD != 1_500_000 || c.Wall != 90*time.Minute {
		t.Fatalf("caps after one extension: %+v", c)
	}
	if c.Passes != 10 {
		t.Fatalf("the pass ceiling is resilience.max_passes and is never extended: %d", c.Passes)
	}
	if !m.Extend() || m.Caps().Tokens != 2000 {
		t.Fatalf("second extension: %+v", m.Caps())
	}
	if m.Extend() || m.Caps().Tokens != 2000 {
		t.Fatalf("a third extension went through: %+v", m.Caps())
	}
	if m.ExtendsLeft() != 0 {
		t.Fatalf("extends left = %d", m.ExtendsLeft())
	}
}

func TestRunCapsRule6_ExtendWarnsAgain(t *testing.T) {
	m := NewMeter(Caps{Tokens: 1000, MaxExtends: 1})
	m.Spend(0, 950)
	m.Spend(0, 100) // hit
	m.Extend()      // 1500; 1050 is below 1350
	if v := m.Check(); v.Level != OK {
		t.Fatalf("after extending: %+v", v)
	}
	if v := m.Spend(0, 300); v.Level != Finalise {
		t.Fatalf("no second warning at the new ceiling: %+v", v)
	}
}

// R7: a model with no known price leaves the cost cap out; tokens still count.
func TestRunCapsRule7_UnpricedLeavesCostOff(t *testing.T) {
	m := NewMeter(Caps{CostMicroUSD: 100, Tokens: 1_000_000})
	u := transcript.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000}
	m.Add(u, nil)
	snap := m.Snapshot()
	if snap.CostMicroUSD != 0 || snap.Tokens != 2_000_000 || !m.Unpriced() {
		t.Fatalf("snapshot %+v unpriced=%v", snap, m.Unpriced())
	}
	p := Price{Input: 1, Output: 2}
	m2 := NewMeter(Caps{CostMicroUSD: 3_000_000})
	if v := m2.Call(u, &p); v.Level != Hit || v.Reason != ReasonCost {
		t.Fatalf("priced call: %+v", v)
	}
	free := Price{}
	m3 := NewMeter(Caps{CostMicroUSD: 1})
	if v := m3.Call(u, &free); v.Level != OK {
		t.Fatalf("a free model costs nothing: %+v", v)
	}
}

// R8: the wall cap counts the time turns run, not the time between them.
func TestRunCapsRule8_WallCountsTurnTime(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewMeter(Caps{Wall: 10 * time.Minute})
	m.SetClock(func() time.Time { return now })
	now = now.Add(5 * time.Hour) // idle before any turn
	if v := m.Check(); v.Level != OK {
		t.Fatalf("idle time counted: %+v", v)
	}
	m.BeginTurn()
	now = now.Add(9 * time.Minute)
	if v := m.Check(); v.Level != Finalise || v.Reason != ReasonWall {
		t.Fatalf("mid-turn: %+v", v)
	}
	m.EndTurn()
	now = now.Add(5 * time.Hour) // idle between turns
	if v := m.Check(); v.Level != OK {
		t.Fatalf("idle time between turns counted: %+v", v)
	}
	m.BeginTurn()
	now = now.Add(2 * time.Minute)
	if v := m.Check(); v.Level != Hit || v.Reason != ReasonWall {
		t.Fatalf("second turn: %+v", v)
	}
	m.EndTurn()
}

// R11: malformed caps fail validation.
func TestRunCapsRule11_CapsValidate(t *testing.T) {
	bad := []Caps{
		{CostMicroUSD: -1}, {Tokens: -1}, {Passes: -1}, {Wall: -time.Second},
		{MaxExtends: -1}, {FinalizeAt: 1}, {FinalizeAt: 1.5}, {FinalizeAt: -0.1}, {FinalizeAt: math.NaN()},
	}
	for _, c := range bad {
		if c.Validate() == nil {
			t.Errorf("%+v passed", c)
		}
	}
	if err := (Caps{Tokens: 5, FinalizeAt: 0.5, MaxExtends: 0}).Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := MeterFor(config.Settings{RunCaps: &config.RunCapsSettings{FinalizeAt: ptr(1.2)}}, "s"); err == nil {
		t.Fatal("MeterFor accepted finalize_at 1.2")
	}
}

func ptr[T any](v T) *T { return &v }

// R12: the counters are saved at 0600 in a 0700 directory and restored for the
// same session id, with the extensions it had used.
func TestRunCapsRule12_StateSavedAndRestored(t *testing.T) {
	home := capsHome(t)
	m := NewMeter(Caps{Tokens: 1000, MaxExtends: 3})
	if m.Persist("sess-1") {
		t.Fatal("restored from nothing")
	}
	m.Spend(2_500, 1100)
	m.Extend()
	m.BeginTurn()
	m.EndTurn()
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "run_caps")
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", di, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "sess-1.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", fi, err)
	}
	m2 := NewMeter(Caps{Tokens: 1000, MaxExtends: 3})
	if !m2.Persist("sess-1") {
		t.Fatal("nothing restored")
	}
	s := m2.Snapshot()
	if s.CostMicroUSD != 2_500 || s.Tokens != 1100 || s.Extends != 1 || m2.Caps().Tokens != 1500 {
		t.Fatalf("restored %+v caps %+v", s, m2.Caps())
	}
	if v := m2.Check(); v.Level == Hit {
		t.Fatalf("a restored, extended run is already past its ceiling: %+v", v)
	}
	// Another session id starts from zero.
	if NewMeter(Caps{Tokens: 1000}).Persist("sess-2") {
		t.Fatal("state leaked between sessions")
	}
}

func TestRunCapsRule12_ForgetRemovesState(t *testing.T) {
	home := capsHome(t)
	m := NewMeter(Caps{Tokens: 10})
	m.Persist("sess-1")
	m.Spend(0, 5)
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	m.Forget()
	if _, err := os.Stat(filepath.Join(home, "run_caps", "sess-1.json")); !os.IsNotExist(err) {
		t.Fatalf("state remained: %v", err)
	}
}

func TestRunCapsRule12_PruneWithSessionRetention(t *testing.T) {
	home := capsHome(t)
	m := NewMeter(Caps{Tokens: 10})
	m.Persist("old")
	_ = m.Save()
	m2 := NewMeter(Caps{Tokens: 10})
	m2.Persist("new")
	_ = m2.Save()
	old := filepath.Join(home, "run_caps", "old.json")
	past := time.Now().Add(-40 * 24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if n := PruneState(28*24*time.Hour, time.Now()); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("the old file stayed")
	}
	if _, err := os.Stat(filepath.Join(home, "run_caps", "new.json")); err != nil {
		t.Fatal("the new file went")
	}
}

// E1: finalize_at outside (0, 1) is refused.
func TestRunCapsEdge1_FinalizeAtRange(t *testing.T) {
	for _, v := range []float64{0, 1, 1.01, -0.5, math.NaN(), math.Inf(1)} {
		s := config.Settings{RunCaps: &config.RunCapsSettings{FinalizeAt: &v}}
		if config.ValidateRunCaps(s) == nil {
			t.Errorf("finalize_at %v accepted", v)
		}
	}
	v := 0.5
	if err := config.ValidateRunCaps(config.Settings{RunCaps: &config.RunCapsSettings{FinalizeAt: &v}}); err != nil {
		t.Fatal(err)
	}
}

// E2: a negative or non-finite cap is refused.
func TestRunCapsEdge2_NegativeAndNaN(t *testing.T) {
	neg, nan, inf := -1.0, math.NaN(), math.Inf(1)
	negI := -5
	for _, rc := range []config.RunCapsSettings{
		{MaxCostUSD: &neg}, {MaxCostUSD: &nan}, {MaxCostUSD: &inf}, {MaxTokens: &negI}, {MaxWallMinutes: &negI}, {MaxExtends: &negI},
	} {
		rc := rc
		if config.ValidateRunCaps(config.Settings{RunCaps: &rc}) == nil {
			t.Errorf("%+v accepted", rc)
		}
	}
}

// E3: a small ceiling still warns before its last unit.
func TestRunCapsEdge3_SmallCapRoundsUp(t *testing.T) {
	if !Reached(7, 8, 0.85) || Reached(6, 8, 0.85) || Reached(1, 0, 0.9) || !Reached(2, 2, 0.9) {
		t.Fatal("Reached is off")
	}
	m := NewMeter(Caps{Passes: 10})
	m.SetPasses(9)
	if v := m.Check(); v.Level != Finalise || v.Reason != ReasonPasses {
		t.Fatalf("%+v", v)
	}
}

// E6: a state file that does not parse is used for nothing and left alone.
func TestRunCapsEdge6_CorruptStateIgnored(t *testing.T) {
	home := capsHome(t)
	dir := filepath.Join(home, "run_caps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"junk.json":     "{not json",
		"extra.json":    `{"tokens":5,"secret":"x"}`,
		"negative.json": `{"tokens":-5}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m := NewMeter(Caps{Tokens: 10})
		if m.Persist(name[:len(name)-5]) {
			t.Errorf("%s restored", name)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("%s was rewritten", name)
		}
	}
}

// E7: an id with unusual characters is hashed, and a symlinked state directory
// is refused.
func TestRunCapsEdge7_OddSessionIDAndSymlink(t *testing.T) {
	home := capsHome(t)
	m := NewMeter(Caps{Tokens: 10})
	m.Persist("../../etc/passwd")
	m.Spend(0, 3)
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(home, "run_caps"))
	if len(entries) != 1 || len(entries[0].Name()) != 64+len(".json") {
		t.Fatalf("entries %v", entries)
	}
	if _, err := os.Stat(filepath.Join(home, "..", "etc")); err == nil {
		t.Fatal("a path escaped the state directory")
	}

	home2 := capsHome(t)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(home2, "run_caps")); err != nil {
		t.Fatal(err)
	}
	m2 := NewMeter(Caps{Tokens: 10})
	m2.Persist("s")
	if err := m2.Save(); err == nil {
		t.Fatal("wrote through a symlinked directory")
	}
	if left, _ := os.ReadDir(target); len(left) != 0 {
		t.Fatalf("wrote into the link target: %v", left)
	}
}

func TestRunCapsRule2_ShareAndDescribe(t *testing.T) {
	m := NewMeter(Caps{Tokens: 1000, CostMicroUSD: 2_000_000})
	m.Spend(1_000_000, 250)
	reason, pct := m.Share()
	if reason != ReasonCost || pct != 50 {
		t.Fatalf("share %s %d", reason, pct)
	}
	if got := m.Describe(); got != "cost $1.00 of $2.00, tokens 250 of 1000" {
		t.Fatalf("describe %q", got)
	}
	if r, p := NewMeter(Caps{}).Share(); r != "" || p != 0 {
		t.Fatalf("an empty meter shares %s %d", r, p)
	}
}
