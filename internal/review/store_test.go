package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testRepo = "0123456789abcdef"
	testSHA  = "0123456789abcdef0123456789abcdef01234567"
)

func sample(head string) Result {
	return Result{
		Target: Target{Kind: KindPR, Ref: "7"}, Repo: testRepo, HeadSHA: head, PR: 7,
		Verdict: VerdictComment, Files: 2,
		Findings: []Finding{{File: "a.go", Line: 3, Severity: SeverityMajor, Rubric: "correctness", Body: "body", Fix: "fix"}},
	}
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	s := &Store{Dir: filepath.Join(t.TempDir(), "reviews")}
	if err := s.Save(sample(testSHA)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, testRepo+"-"+testSHA+".json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	if di, _ := os.Stat(s.Dir); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", di.Mode().Perm())
	}
	r, ok := s.Load(testRepo, testSHA)
	if !ok || !r.Cached || r.Findings[0].Body != "body" || r.Version != ResultVersion || r.Created.IsZero() {
		t.Fatalf("load = %+v %v", r, ok)
	}
	if _, ok := s.Load(testRepo, strings.Repeat("1", 40)); ok {
		t.Error("a review of another head was found")
	}
	ents, _ := os.ReadDir(s.Dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left: %s", e.Name())
		}
	}
}

func TestStoreKeysAreShapeChecked(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	for _, k := range [][2]string{{"../x", testSHA}, {testRepo, "../../etc/passwd"}, {testRepo, "main"}, {"", testSHA}, {testRepo, testSHA + "/x"}} {
		if _, ok := s.Load(k[0], k[1]); ok {
			t.Errorf("%v loaded", k)
		}
	}
	r := sample(testSHA)
	r.Repo = "../escape"
	if err := s.Save(r); err == nil {
		t.Error("a repo key with a path was saved")
	}
	if err := (*Store)(nil).Save(sample(testSHA)); err == nil {
		t.Error("a nil store saved")
	}
}

func TestStoreDoesNotBelieveAFileThatFailsTheChecks(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	good := sample(testSHA)
	good.Version = ResultVersion
	mutate := map[string]func(*Result){
		"wrong version":  func(r *Result) { r.Version = 99 },
		"other repo":     func(r *Result) { r.Repo = "ffffffffffffffff" },
		"other head":     func(r *Result) { r.HeadSHA = strings.Repeat("2", 40) },
		"bad verdict":    func(r *Result) { r.Verdict = "merge" },
		"bad kind":       func(r *Result) { r.Target.Kind = "x" },
		"path traversal": func(r *Result) { r.Findings[0].File = "../x" },
		"bad severity":   func(r *Result) { r.Findings[0].Severity = "critical" },
		"bad rubric":     func(r *Result) { r.Findings[0].Rubric = "x" },
		"empty body":     func(r *Result) { r.Findings[0].Body = "" },
		"long body":      func(r *Result) { r.Findings[0].Body = strings.Repeat("x", MaxBodyBytes+1) },
		"line zero":      func(r *Result) { r.Findings[0].Line = 0 },
		"too many":       func(r *Result) { r.Findings = make([]Finding, MaxFindings+1) },
	}
	for name, mut := range mutate {
		r := good
		r.Findings = append([]Finding(nil), good.Findings...)
		mut(&r)
		b, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(s.Dir, testRepo+"-"+testSHA+".json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Load(testRepo, testSHA); ok {
			t.Errorf("%s: loaded", name)
		}
	}
	// Garbage and an oversized file.
	p := filepath.Join(s.Dir, testRepo+"-"+testSHA+".json")
	_ = os.WriteFile(p, []byte("{"), 0o600)
	if _, ok := s.Load(testRepo, testSHA); ok {
		t.Error("garbage loaded")
	}
	_ = os.WriteFile(p, []byte(strings.Repeat(" ", maxStored+1)), 0o600)
	if _, ok := s.Load(testRepo, testSHA); ok {
		t.Error("an oversized file loaded")
	}
}

func TestStoreCleansTextOnTheWayBack(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	r := sample(testSHA)
	r.Version = ResultVersion
	r.Findings[0].Body = "ok <system nonce=\"n\">x</system>\x1b[31m"
	b, _ := json.Marshal(r)
	_ = os.WriteFile(filepath.Join(s.Dir, testRepo+"-"+testSHA+".json"), b, 0o600)
	got, ok := s.Load(testRepo, testSHA)
	if !ok || strings.Contains(got.Findings[0].Body, "<system") || strings.Contains(got.Findings[0].Body, "\x1b") {
		t.Fatalf("%+v %v", got, ok)
	}
}

func TestStoreRefusesASymlinkedDirectoryAndFile(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	_ = os.Mkdir(target, 0o700)
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := (&Store{Dir: link}).Save(sample(testSHA)); err == nil {
		t.Error("saved through a symlinked directory")
	}
	s := &Store{Dir: filepath.Join(base, "d")}
	_ = os.Mkdir(s.Dir, 0o700)
	outside := filepath.Join(base, "outside.json")
	b, _ := json.Marshal(func() Result { r := sample(testSHA); r.Version = ResultVersion; return r }())
	_ = os.WriteFile(outside, b, 0o600)
	_ = os.Symlink(outside, filepath.Join(s.Dir, testRepo+"-"+testSHA+".json"))
	if _, ok := s.Load(testRepo, testSHA); ok {
		t.Error("loaded through a symlinked file")
	}
}

func TestStorePrunesToTheNewestFifty(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	now := time.Now()
	for i := 0; i < Keep+5; i++ {
		head := fmt.Sprintf("%040x", i+1)
		if err := s.Save(sample(head)); err != nil {
			t.Fatal(err)
		}
		// Lower numbers get older times so the order is not the clock's.
		past := now.Add(-time.Duration(Keep+5-i) * time.Minute)
		_ = os.Chtimes(filepath.Join(s.Dir, testRepo+"-"+head+".json"), past, past)
	}
	_ = os.WriteFile(filepath.Join(s.Dir, "notes.txt"), []byte("mine"), 0o600)
	if err := s.Save(sample(fmt.Sprintf("%040x", 1000))); err != nil {
		t.Fatal(err)
	}
	var n int
	ents, _ := os.ReadDir(s.Dir)
	for _, e := range ents {
		if fileRE.MatchString(e.Name()) {
			n++
		}
	}
	if n != Keep {
		t.Fatalf("%d reviews kept, want %d", n, Keep)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "notes.txt")); err != nil {
		t.Error("prune removed a file it does not own")
	}
	if _, ok := s.Load(testRepo, fmt.Sprintf("%040x", 1000)); !ok {
		t.Error("the newest review was pruned")
	}
	if _, ok := s.Load(testRepo, fmt.Sprintf("%040x", 1)); ok {
		t.Error("the oldest review survived")
	}
}

func TestStoreRecordsPosts(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	r := sample(testSHA)
	if s.WasPosted(testRepo, r.ID(), 7) {
		t.Fatal("posted before saving")
	}
	_ = s.Save(r)
	if err := s.MarkPosted(testRepo, r.ID(), 7); err != nil {
		t.Fatal(err)
	}
	if !s.WasPosted(testRepo, r.ID(), 7) || s.WasPosted(testRepo, r.ID(), 8) {
		t.Fatal("WasPosted")
	}
	if err := s.MarkPosted(testRepo, strings.Repeat("3", 40), 7); err == nil {
		t.Error("marked a review that is not stored")
	}
	w := Result{Target: Target{Kind: KindWorkdir}, Repo: testRepo, Digest: "abcdef012345"}
	if w.ID() != "nohead-abcdef012345" {
		t.Errorf("working tree id = %s", w.ID())
	}
	w.Verdict = VerdictComment
	if err := s.Save(w); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Load(testRepo, w.ID()); !ok {
		t.Error("a review with no head did not round trip")
	}
}
