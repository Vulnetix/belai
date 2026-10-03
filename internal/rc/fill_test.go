package rc

import (
	"testing"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// A crew request with fill starts the crew with -fill; without it, the whole crew.
func TestDaemonPassesFillOnACrewStart(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	proj := t.TempDir()
	real, _ := Normalize(proj)
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got WorkerStart
	d, err := New(Options{
		Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "proj", Source: SourceTrusted}},
		Inventory: func() Inventory {
			return Inventory{Crews: []sessionsync.RCCrew{{Name: "aws-infra", Members: []sessionsync.RCMember{{Profile: "tf", Replicas: 2}}}}}
		},
		StartWorkers: func(w WorkerStart) (string, error) { got = w; return "ok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, refused := d.startWorkers(sessionsync.Dispatch{Kind: "crew", Cwd: proj, Crew: "aws-infra", Fill: true}); refused != "" {
		t.Fatalf("start refused: %s", refused)
	}
	if got.Crew != "aws-infra" || !got.Fill {
		t.Fatalf("start = %+v, want the crew with Fill", got)
	}
	if _, refused := d.startWorkers(sessionsync.Dispatch{Kind: "crew", Cwd: proj, Crew: "aws-infra"}); refused != "" {
		t.Fatalf("start refused: %s", refused)
	}
	if got.Fill {
		t.Fatal("a crew request without fill started with -fill")
	}
}

// A daemon run with --web-controls starts its workers taking session controls,
// and passes on guardrails-off only when it was allowed too.
func TestDaemonStartsWorkersWithItsWebControls(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	proj := t.TempDir()
	real, _ := Normalize(proj)
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ controls, off, wantOff bool }{{true, false, false}, {true, true, true}, {false, true, false}} {
		var got WorkerStart
		d, err := New(Options{
			Client: client, HostID: testHost, Dirs: []Dir{{Path: real, Name: "proj", Source: SourceTrusted}},
			Controls: c.controls, GuardrailsOff: c.off,
			Inventory: func() Inventory {
				return Inventory{Profiles: []sessionsync.RCProfile{{Name: "belai:builder"}}}
			},
			StartWorkers: func(w WorkerStart) (string, error) { got = w; return "ok", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, refused := d.startWorkers(sessionsync.Dispatch{Kind: "worker", Cwd: proj, Profile: "belai:builder"}); refused != "" {
			t.Fatalf("start refused: %s", refused)
		}
		if got.Controls != c.controls || got.GuardrailsOff != c.wantOff {
			t.Errorf("controls %v off %v: start = %+v", c.controls, c.off, got)
		}
	}
}
