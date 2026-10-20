package rc

import (
	"context"
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/schedule"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func TestRotationPrefersTheHostsOwnProfilesThenBuiltinsFindersFirst(t *testing.T) {
	builtins := Inventory{Profiles: []sessionsync.RCProfile{
		{Name: "belai:builder", Builtin: true}, {Name: "belai:zeta", Builtin: true},
		{Name: "belai:vuln-scout", Builtin: true}, {Name: "belai:scout", Builtin: true}, {Name: schedule.RotateProfile, Builtin: true},
	}}
	if got, want := rotationProfiles(builtins), []string{"belai:vuln-scout", "belai:scout", "belai:builder", "belai:zeta"}; !slices.Equal(got, want) {
		t.Fatalf("built-ins %v, want %v", got, want)
	}
	own := builtins
	own.Profiles = append(slices.Clone(own.Profiles), sessionsync.RCProfile{Name: "triage"}, sessionsync.RCProfile{Name: "docs"})
	if got := rotationProfiles(own); !slices.Equal(got, []string{"docs", "triage"}) {
		t.Fatalf("own profiles %v", got)
	}
	if _, ok := rotationEntry(Inventory{}); ok {
		t.Fatal("nothing to rotate, yet the rotation is advertised")
	}
	if e, ok := rotationEntry(builtins); !ok || e.Name != schedule.RotateProfile {
		t.Fatalf("rotation entry %+v", e)
	}
}

func TestRotationVisitsEveryDirectoryWithEachProfileInTurn(t *testing.T) {
	ps, ds := []string{"a", "b"}, []string{"x", "y", "z"}
	var got []string
	for turn := range 7 {
		p, d, ok := rotationPick(turn, ps, ds)
		if !ok {
			t.Fatal("no pick")
		}
		got = append(got, p+d)
	}
	if want := []string{"ax", "ay", "az", "bx", "by", "bz", "ax"}; !slices.Equal(got, want) {
		t.Fatalf("turns %v, want %v", got, want)
	}
	if _, _, ok := rotationPick(0, nil, ds); ok {
		t.Fatal("picked with no profiles")
	}
}

// A rotating schedule starts the next built-in in the next directory at each
// run, and its outcome is read from the worker that turn started.
func TestRotatingScheduleCyclesBuiltinsAcrossDirectories(t *testing.T) {
	h := newSchedHarness(t)
	other, _ := Normalize(t.TempDir())
	h.d.o.Dirs = append(h.d.o.Dirs, Dir{Path: other, Name: "other", Source: SourceTrusted})
	h.d.o.Inventory = func() Inventory {
		return Inventory{Profiles: []sessionsync.RCProfile{{Name: "belai:scout", Builtin: true}, {Name: "belai:vuln-scout", Builtin: true}}}
	}
	if _, err := h.st.Add(schedule.Record{ID: schedA, Profile: schedule.RotateProfile, Cron: "0 * * * *", Dir: h.dir, Enabled: true}, h.now); err != nil {
		t.Fatal(err)
	}
	h.d.fireDue(context.Background(), h.now) // sets the next run
	var got []string
	for hour := 10; hour <= 12; hour++ {
		h.at(hour, 0, 1)
		h.d.fireDue(context.Background(), h.now)
		w := h.starts[len(h.starts)-1]
		got = append(got, w.Profile+"@"+w.Cwd)
		if !w.Drain {
			t.Fatal("a scheduled rotation must drain")
		}
	}
	want := []string{"belai:vuln-scout@" + h.dir, "belai:vuln-scout@" + other, "belai:scout@" + h.dir}
	if !slices.Equal(got, want) {
		t.Fatalf("turns %v, want %v", got, want)
	}
	r := h.get(schedA)
	if r.Turn != 3 || r.RanProfile != "belai:scout" || r.RanDir != h.dir || r.LastStatus != schedule.StatusStarted {
		t.Fatalf("record %+v", r)
	}
	if p, d := ranAs(r); p != "belai:scout" || d != h.dir {
		t.Fatalf("ranAs %s %s", p, d)
	}
}

func TestRotationIsNotAProfileAWorkerRequestCanStart(t *testing.T) {
	h := newSchedHarness(t)
	if _, why := h.d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: h.dir, Profile: schedule.RotateProfile}); why == "" {
		t.Fatal("belai:rotate started as a worker")
	}
}
