package vaultenv

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

const secret = "s3cr3t-TOKEN/value+with=chars&more"

func storeWith(vars ...Var) *Store {
	s := &Store{}
	s.Replace(vars, time.Now().Add(time.Hour))
	return s
}

func TestScrubReplacesTheValueAndItsEncodedForms(t *testing.T) {
	s := storeWith(Var{Name: "NPM_TOKEN", Value: secret})
	b := []byte(secret)
	forms := map[string]string{
		"raw":       secret,
		"base64":    base64.StdEncoding.EncodeToString(b),
		"rawbase64": base64.RawStdEncoding.EncodeToString(b),
		"url64":     base64.URLEncoding.EncodeToString(b),
		"hex":       hex.EncodeToString(b),
		"HEX":       strings.ToUpper(hex.EncodeToString(b)),
		"query":     url.QueryEscape(secret),
		"json":      `s3cr3t-TOKEN/value+with=chars&more`,
	}
	for name, form := range forms {
		got := s.Scrub("before " + form + " after")
		if strings.Contains(got, form) || !strings.Contains(got, "[vault:NPM_TOKEN]") {
			t.Errorf("%s form was not scrubbed: %q", name, got)
		}
	}
	if got := s.Scrub("nothing to hide here"); got != "nothing to hide here" {
		t.Fatalf("clean text changed: %q", got)
	}
}

func TestScrubCountsHitsAndDrains(t *testing.T) {
	s := storeWith(Var{Name: "A_TOKEN", Value: "aaaaaaaa-1111"}, Var{Name: "B_TOKEN", Value: "bbbbbbbb-2222"})
	s.Scrub("aaaaaaaa-1111 and aaaaaaaa-1111 and bbbbbbbb-2222")
	h := s.Hits()
	if h["A_TOKEN"] != 2 || h["B_TOKEN"] != 1 {
		t.Fatalf("hits %v", h)
	}
	if again := s.Hits(); len(again) != 0 {
		t.Fatalf("hits reset after being read: %v", again)
	}
}

func TestMultiLineValueIsScrubbedLineByLine(t *testing.T) {
	key := "-----BEGIN KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END KEY-----"
	s := storeWith(Var{Name: "DEPLOY_KEY", Value: key})
	got := s.Scrub("saw: MIIEvQIBADANBgkqhkiG9w0BAQEFAASC in a log line")
	if strings.Contains(got, "MIIEvQIBADAN") {
		t.Fatalf("a line of a multi-line value leaked: %q", got)
	}
}

func TestShortValuesAreNeverInjectedOrScrubbed(t *testing.T) {
	s := storeWith(Var{Name: "FLAG", Value: "true"}, Var{Name: "OK_TOKEN", Value: "longenough-value"})
	if got := s.Environ(time.Now()); len(got) != 1 || got[0] != "OK_TOKEN=longenough-value" {
		t.Fatalf("environ %v", got)
	}
	if got := s.Scrub("true story"); got != "true story" {
		t.Fatalf("a short value must not hide ordinary words: %q", got)
	}
}

func TestExpiryAndLeaseStopInjection(t *testing.T) {
	now := time.Now()
	s := &Store{}
	s.Replace([]Var{
		{Name: "SOON_TOKEN", Value: "soon-value-1234", ExpiresAt: now.Add(time.Minute)},
		{Name: "FOREVER_TOKEN", Value: "forever-value-1234"},
	}, now.Add(time.Hour))
	if n := s.Names(now); len(n) != 2 {
		t.Fatalf("names %v", n)
	}
	if n := s.Names(now.Add(2 * time.Minute)); len(n) != 1 || n[0] != "FOREVER_TOKEN" {
		t.Fatalf("an expired variable is not injected: %v", n)
	}
	if s.HasActive(now.Add(2 * time.Hour)) {
		t.Fatal("nothing is injected after the lease ends")
	}
	// Dropped values are still scrubbed from output.
	if got := s.Scrub("log: soon-value-1234"); strings.Contains(got, "soon-value-1234") {
		t.Fatalf("an expired value must still be scrubbed: %q", got)
	}
}

func TestReplaceStopsInjectingWhatTheVaultNoLongerGrants(t *testing.T) {
	s := storeWith(Var{Name: "OLD_TOKEN", Value: "old-token-value-1"})
	s.Replace(nil, time.Now().Add(time.Hour))
	if s.HasActive(time.Now()) || len(s.Environ(time.Now())) != 0 {
		t.Fatal("a revoked variable is dropped at the next lease")
	}
	if got := s.Scrub("old-token-value-1"); got == "old-token-value-1" {
		t.Fatal("but its value is still scrubbed")
	}
}

func TestVarNeverPrintsItsValue(t *testing.T) {
	v := Var{Name: "X_TOKEN", Value: "do-not-print-this-1"}
	if s := v.String(); strings.Contains(s, "do-not-print") {
		t.Fatalf("%q", s)
	}
}

func TestWriterCatchesAValueSplitAcrossWrites(t *testing.T) {
	s := storeWith(Var{Name: "SPLIT_TOKEN", Value: "split-across-writes-9876"})
	var out bytes.Buffer
	w := NewWriter(&out, s)
	for _, chunk := range []string{"start split-acr", "oss-writ", "es-9876 end"} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "split-across") || got != "start [vault:SPLIT_TOKEN] end" {
		t.Fatalf("got %q", got)
	}
}

func TestWriterPassesThroughWhenThereIsNothingToHide(t *testing.T) {
	var out bytes.Buffer
	w := NewWriter(&out, &Store{})
	_, _ = w.Write([]byte("hello "))
	_, _ = w.Write([]byte("world"))
	_ = w.Close()
	if out.String() != "hello world" {
		t.Fatalf("%q", out.String())
	}
}

func TestWriterKeepsEveryByteInOrder(t *testing.T) {
	s := storeWith(Var{Name: "K_TOKEN", Value: "k-token-value-xyz"})
	var out bytes.Buffer
	w := NewWriter(&out, s)
	text := strings.Repeat("line of ordinary output\n", 200) + "k-token-value-xyz\n" + strings.Repeat("more\n", 50)
	for i := 0; i < len(text); i += 7 {
		end := i + 7
		if end > len(text) {
			end = len(text)
		}
		_, _ = w.Write([]byte(text[i:end]))
	}
	_ = w.Close()
	want := strings.ReplaceAll(text, "k-token-value-xyz", "[vault:K_TOKEN]")
	if out.String() != want {
		t.Fatalf("output differs: %d bytes vs %d", out.Len(), len(want))
	}
}

func TestScrubBytesCatchesTheJSONEscapedFormInATranscriptLine(t *testing.T) {
	s := storeWith(Var{Name: "DB_URL", Value: `postgres://u:p"w@host/db?x=1&y=2`})
	line := []byte(`{"type":"tool_result","content":"postgres://u:p\"w@host/db?x=1&y=2"}`)
	got := string(s.ScrubBytes(line))
	if strings.Contains(got, "p\\\"w@host") {
		t.Fatalf("transcript line leaked: %s", got)
	}
}
