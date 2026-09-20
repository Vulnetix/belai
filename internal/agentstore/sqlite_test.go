package agentstore

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func TestGooseTurnsParse(t *testing.T) {
	out := "user|[{\"type\":\"text\",\"text\":\"hello nonce\"}]|1783998487\n" +
		"assistant|[{\"type\":\"text\",\"text\":\"world\"}]|1783998490\n"
	turns := gooseTurns(out)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Text != "hello nonce" {
		t.Fatalf("turn 0 = %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Text != "world" {
		t.Fatalf("turn 1 = %+v", turns[1])
	}
	if turns[0].At.Unix() != 1783998487 {
		t.Fatalf("At = %v", turns[0].At)
	}
}

func TestOpencodeTurnsParse(t *testing.T) {
	out := `{"id":"m1","role":"user"}|{"type":"text","text":"hello"}`
	out += "\n" + `{"id":"m1","role":"user"}|{"type":"text","text":" nonce"}`
	out += "\n" + `{"id":"m2","role":"assistant"}|{"type":"text","text":"world"}`
	turns := opencodeTurns(out)
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if turns[0].Role != "user" || turns[0].Text != "hello\n nonce" {
		t.Fatalf("turn 0 = %+v", turns[0])
	}
	if turns[1].Role != "assistant" || turns[1].Text != "world" {
		t.Fatalf("turn 1 = %+v", turns[1])
	}
}

func TestParseSQLiteSessions(t *testing.T) {
	out := "id1|/home/u/proj|1783998487488\nid2|/home/u/other|1783998487488\n"
	srcs := parseSQLiteSessions(out, "goose", FormatSQLiteGoose, "/tmp/sessions.db", time.Time{})
	if len(srcs) != 2 {
		t.Fatalf("srcs = %d, want 2", len(srcs))
	}
	if srcs[0].SessionID != "id1" || srcs[0].Project != "/home/u/proj" {
		t.Fatalf("src 0 = %+v", srcs[0])
	}
	if srcs[1].Agent != "goose" || srcs[1].Format != FormatSQLiteGoose {
		t.Fatalf("src 1 = %+v", srcs[1])
	}
}

// TestGooseAdapterAgainstSQLite exercises the real sqlite3 shell-out when the
// binary is available; otherwise it skips (the parsing above still pins the
// dialect).
func TestGooseAdapterAgainstSQLite(t *testing.T) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not on PATH")
	}
	db := filepath.Join(t.TempDir(), "sessions.db")
	run := func(sql string) {
		t.Helper()
		ec := exec.Command(sqlite, db, sql)
		if out, err := ec.CombinedOutput(); err != nil {
			t.Fatalf("sqlite3 %s: %v\n%s", sql, err, out)
		}
	}
	run("CREATE TABLE sessions (id TEXT PRIMARY KEY, working_dir TEXT NOT NULL, updated_at TIMESTAMP);")
	run("CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content_json TEXT NOT NULL, created_timestamp INTEGER NOT NULL);")
	run("INSERT INTO sessions VALUES ('s1','/home/u/proj','2026-09-08');")
	run("INSERT INTO messages (session_id, role, content_json, created_timestamp) VALUES ('s1','user','[{\"type\":\"text\",\"text\":\"where is the nonce\"}]',1783998487);")

	a := gooseAdapter{sqlite: sqlite}
	srcs, err := a.Sources(db)
	if err != nil {
		t.Fatalf("Sources: %v", err)
	}
	if len(srcs) != 1 || srcs[0].SessionID != "s1" || srcs[0].Project != "/home/u/proj" {
		t.Fatalf("srcs = %+v", srcs)
	}
	turns, err := a.Turns(srcs[0], 0, 0)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	if len(turns) != 1 || turns[0].Text != "where is the nonce" {
		t.Fatalf("turns = %+v", turns)
	}
	hits, err := a.Scan(context.Background(), srcs[0], regexp.MustCompile("nonce"), DefaultCaps())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(hits) != 1 || hits[0].SessionID != "s1" {
		t.Fatalf("hits = %+v", hits)
	}
}
