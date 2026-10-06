package audit

import (
	"os"
	"strings"
	"testing"
	"time"
)

// serverFixture emits one event of every kind, with the facts a real run records,
// through a Recorder on a fixed clock. The stream is deterministic, so vdb-site
// can keep it as testdata and prove that what this package writes is what its
// handler accepts, byte for byte.
func serverFixture(t *testing.T) *Recorder {
	t.Helper()
	r, err := Open(t.TempDir(), "build-01")
	if err != nil {
		t.Fatal(err)
	}
	tick := int64(1_790_000_000_000)
	r.now = func() time.Time { tick += 1000; return time.UnixMilli(tick) }

	const (
		card    = "3f9a2c00-1111-4222-8333-444455556666"
		session = "22222222-2222-4222-8222-222222222222"
		commit  = "0123456789abcdef0123456789abcdef01234567"
		base    = "89abcdef0123456789abcdef0123456789abcdef"
		scan    = "fedcba9876543210fedcba9876543210fedcba98"
	)
	emit := func(f Fact) { r.Emit(f) }
	emit(Fact{Kind: HostFirstSeen, ActorKind: ActorHarness, Data: map[string]string{"version": "1.0.0", "os": "linux"}})
	emit(Fact{Kind: HostVersionChange, ActorKind: ActorHarness, Data: map[string]string{"version": "1.1.0", "previous": "1.0.0", "os": "linux"}})
	emit(Fact{Kind: HostRCOnline, ActorKind: ActorHarness, Data: map[string]string{"version": "1.1.0"}})
	emit(Fact{Kind: HostDispatch, ActorKind: ActorWeb, Data: map[string]string{"dispatch": "crew", "status": "started"}})
	emit(Fact{Kind: HostSchedule, ActorKind: ActorHarness, Outcome: "started",
		Data: map[string]string{"schedule": "55555555-5555-4555-8555-555555555555", "profile": "belai:security", "status": "started"}})
	emit(Fact{Kind: HostTeleport, ActorKind: ActorHuman, SessionID: "33333333-3333-4333-8333-333333333333", Outcome: "completed",
		Data: map[string]string{"from": session, "status": "completed"}})
	emit(Fact{Kind: WorkerStarted, ActorKind: ActorAgent, Actor: "belai:security", Crew: "belai:security", Repo: "belai", Outcome: "started",
		Data: map[string]string{"worker": "security-1a2b", "state": "started"}})
	emit(Fact{Kind: CardClaimed, ActorKind: ActorAgent, Actor: "belai:security", Crew: "belai:security", ItemID: card, Repo: "belai",
		VulnID: "CVE-2026-12345", SeenRef: scan, Outcome: "claimed", Data: map[string]string{"worker": "security-1a2b", "list": "backlog", "attempt": "0"}})
	emit(Fact{Kind: RepoCommit, ActorKind: ActorAgent, Actor: "belai:security", ItemID: card, SessionID: session, Repo: "belai",
		Branch: "belai/K-3f9a2c/a1", Commit: commit, BaseCommit: base, VulnID: "CVE-2026-12345", Outcome: "committed",
		Data: map[string]string{"worker": "security-1a2b", "files": "3"}})
	emit(Fact{Kind: GateVerified, ActorKind: ActorAgent, Actor: "belai:security", ItemID: card, SessionID: session, Repo: "belai",
		Branch: "belai/K-3f9a2c/a1", Commit: commit, BaseCommit: base, VulnID: "CVE-2026-12345", Outcome: "met",
		Data: map[string]string{"worker": "security-1a2b", "gate": "G1", "suite": "go", "status": "met", "exit": "0"}})
	emit(Fact{Kind: VEXWritten, ActorKind: ActorAgent, Actor: "belai:security", ItemID: card, SessionID: session, Repo: "belai",
		Commit: commit, VulnID: "CVE-2026-12345", Verdict: "false_positive", Outcome: "written",
		Data: map[string]string{"worker": "security-1a2b", "justification": "vulnerable_code_not_in_execute_path", "path": ".vulnetix/vex/CVE-2026-12345.openvex.json"}})
	emit(Fact{Kind: RepoPublish, ActorKind: ActorAgent, Actor: "belai:security", ItemID: card, SessionID: session, Repo: "belai",
		Branch: "belai/K-3f9a2c/a1", VulnID: "CVE-2026-12345", Outcome: "draft",
		Data: map[string]string{"worker": "security-1a2b", "pr": "https://github.com/vulnetix/belai/pull/12", "forge": "github.com"}})
	emit(Fact{Kind: CardReleased, ActorKind: ActorAgent, Actor: "belai:security", ItemID: card, SessionID: session, Repo: "belai",
		Branch: "belai/K-3f9a2c/a1", VulnID: "CVE-2026-12345", Verdict: "false_positive", Outcome: "completed",
		Data: map[string]string{"worker": "security-1a2b", "list": "review", "attempt": "0"}})
	emit(Fact{Kind: CardLeaseLapse, ActorKind: ActorHarness, ItemID: card, Repo: "belai", VulnID: "CVE-2026-12345", Outcome: "lapsed",
		Data: map[string]string{"worker": "security-9z9z", "list": "backlog", "attempt": "1"}})
	emit(Fact{Kind: FindingCarded, ActorKind: ActorHarness, ItemID: card, Repo: "belai", VulnID: "CVE-2026-12345", SeenRef: scan,
		Outcome: "created", Data: map[string]string{"change": "created"}})
	emit(Fact{Kind: FindingReconciled, ActorKind: ActorHarness, ItemID: card, Repo: "belai", VulnID: "CVE-2026-12345", SeenRef: scan,
		Verdict: "fixed", Outcome: "gone", Data: map[string]string{"change": "gone"}})
	emit(Fact{Kind: WorkerState, ActorKind: ActorAgent, Actor: "belai:security", Repo: "belai", Outcome: "paused",
		Data: map[string]string{"worker": "security-1a2b", "state": "paused"}})
	emit(Fact{Kind: WorkerStopped, ActorKind: ActorAgent, Actor: "belai:security", Repo: "belai", Outcome: "stopped",
		Data: map[string]string{"worker": "security-1a2b", "state": "stopped"}})
	emit(Fact{Kind: HostRCOffline, ActorKind: ActorHarness, Data: map[string]string{"reason": "stopped"}})
	return r
}

func TestServerFixtureCoversEveryKindAndVerifies(t *testing.T) {
	r := serverFixture(t)
	events := readEvents(t, r.Path())
	seen := map[string]bool{}
	for _, e := range events {
		seen[e.Kind] = true
	}
	for _, k := range Kinds() {
		if !seen[string(k)] {
			t.Errorf("the fixture has no %s event", k)
		}
	}
	if at := Verify(events); at != -1 {
		t.Fatalf("the fixture's chain breaks at %d", at)
	}
}

// TestWriteServerFixture regenerates the stream vdb-site keeps as testdata
// (api/internal/handler/testdata/belai_audit_stream.jsonl). It is skipped unless
// BELAI_AUDIT_FIXTURE names the file to write:
//
//	BELAI_AUDIT_FIXTURE=../vdb-site/api/internal/handler/testdata/belai_audit_stream.jsonl \
//	  go test ./internal/audit -run TestWriteServerFixture
//
// The hashes change only when an event's canonical form does, which is a change
// to the chain and to the server's verification together.
func TestWriteServerFixture(t *testing.T) {
	path := os.Getenv("BELAI_AUDIT_FIXTURE")
	if path == "" {
		t.Skip("set BELAI_AUDIT_FIXTURE to regenerate the vdb-site fixture")
	}
	r := serverFixture(t)
	data, err := os.ReadFile(r.Path())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(data), "\n"); n < len(Kinds()) {
		t.Fatalf("the fixture is shorter than the kinds: %d lines", n)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
