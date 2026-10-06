package session

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

func TestATranscriptNeverHoldsAVaultValue(t *testing.T) {
	secret := `postgres://app:pa"ss@db.internal/prod?sslmode=require&x=1`
	vaultenv.Default.Replace([]vaultenv.Var{{Name: "STAGING_DB_URL", Value: secret}}, time.Now().Add(time.Hour))
	defer vaultenv.Default.Replace(nil, time.Time{})

	s := NewStoreAt(t.TempDir())
	key, _ := KeyFor(t.TempDir())
	id := MustID()
	if err := s.AppendTo(key, id, Entry{ID: "t1", Type: "tool", Role: "tool", Content: "connecting to " + secret + " ok"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(s.SessionPath(key, id))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "pa\\\"ss@db.internal") || strings.Contains(text, "pa\"ss@db.internal") || strings.Contains(text, "db.internal/prod") {
		t.Fatalf("the transcript line holds the value:\n%s", text)
	}
	if !strings.Contains(text, "[vault:STAGING_DB_URL]") {
		t.Fatalf("expected the placeholder in the line:\n%s", text)
	}
	// And the line still parses.
	got, err := s.ReadFrom(key, id)
	if err != nil || len(got) != 1 || !strings.Contains(got[0].Content, "[vault:STAGING_DB_URL]") {
		t.Fatalf("%+v %v", got, err)
	}
}
