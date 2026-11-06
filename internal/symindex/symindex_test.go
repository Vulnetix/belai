package symindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "srv/server.go", "package srv\n\ntype Server struct{}\n\nfunc (s *Server) Handle() {}\n\nfunc handle() {}\n\nfunc HandleRequest() {}\n\nconst Max = 1\n")
	write(t, root, "srv/client.go", "package srv\n\ntype Client struct{}\n\nfunc (c *Client) Handle() {}\n")
	write(t, root, "web/app.py", "class Widget:\n    def render(self):\n        pass\n")
	write(t, root, "README.md", "type Fake struct\nfunc notreal()\n")
	write(t, root, ".env", "func secret()\n")
	write(t, root, "node_modules/x/index.js", "function dep() {}\n")
	write(t, root, "ignored/skip.go", "package ignored\nfunc Skipped() {}\n")
	write(t, root, ".gitignore", "ignored/\n")
	write(t, root, "data.bin", "\x00\x01func nope()")
	write(t, root, "key.pem", "func pem()")
	if err := os.Symlink(filepath.Join(root, "srv/server.go"), filepath.Join(root, "link.go")); err != nil {
		t.Log("no symlinks:", err)
	}
	return root
}

func build(t *testing.T, root string, prev *Index, lim Limits) *Index {
	t.Helper()
	ix, err := Build(context.Background(), root, prev, lim)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func names(hs []Hit) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = h.Path + ":" + h.Name
		if h.Container != "" {
			out[i] = h.Path + ":" + h.Container + "." + h.Name
		}
	}
	return out
}

func TestBuildIndexesOnlyEligibleSourceFiles(t *testing.T) {
	ix := build(t, fixture(t), nil, Limits{})
	for _, rel := range []string{"README.md", ".env", "node_modules/x/index.js", "ignored/skip.go", "data.bin", "key.pem", "link.go", ".gitignore"} {
		if ix.Has(rel) {
			t.Errorf("%s was indexed", rel)
		}
	}
	for _, rel := range []string{"srv/server.go", "srv/client.go", "web/app.py"} {
		if !ix.Has(rel) {
			t.Errorf("%s was not indexed", rel)
		}
	}
	for _, name := range []string{"Fake", "notreal", "secret", "dep", "Skipped", "nope", "pem"} {
		if got := ix.Lookup(name, "", 10); len(got) != 0 {
			t.Errorf("%s found: %v", name, names(got))
		}
	}
}

func TestOutlineIsInSourceOrderWithKindsAndSpans(t *testing.T) {
	ix := build(t, fixture(t), nil, Limits{})
	got, ok := ix.Outline("srv/server.go")
	if !ok {
		t.Fatal("no outline")
	}
	want := []Symbol{
		{Name: "Server", Kind: "struct", Line: 3, EndLine: 3},
		{Name: "Handle", Kind: "method", Container: "Server", Line: 5, EndLine: 5},
		{Name: "handle", Kind: "func", Line: 7, EndLine: 7},
		{Name: "HandleRequest", Kind: "func", Line: 9, EndLine: 9},
		{Name: "Max", Kind: "const", Line: 11, EndLine: 11},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("outline:\n got %+v\nwant %+v", got, want)
	}
	if _, ok := ix.Outline("nope.go"); ok {
		t.Error("an unindexed file has an outline")
	}
	got[0].Name = "Changed"
	if again, _ := ix.Outline("srv/server.go"); again[0].Name != "Server" {
		t.Error("Outline returned the index's own slice")
	}
}

func TestLookupRanksExactThenFoldThenPrefixThenContains(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\nfunc Handle() {}\nfunc handle() {}\nfunc HandleRequest() {}\nfunc RehandleAll() {}\nfunc Other() {}\n")
	ix := build(t, root, nil, Limits{})
	want := []string{"a.go:Handle", "a.go:handle", "a.go:HandleRequest", "a.go:RehandleAll"}
	if got := names(ix.Lookup("Handle", "", 10)); !reflect.DeepEqual(got, want) {
		t.Errorf("rank = %v, want %v", got, want)
	}
	if got := names(ix.Lookup("Handle", "", 2)); len(got) != 2 {
		t.Errorf("limit ignored: %v", got)
	}
	if got := ix.Lookup("Handle", "", 0); got != nil {
		t.Errorf("limit 0 = %v", got)
	}
	if got := ix.Lookup("", "", 5); got != nil {
		t.Errorf("empty name = %v", got)
	}
}

func TestLookupFiltersByKindAndReceiver(t *testing.T) {
	ix := build(t, fixture(t), nil, Limits{})
	if got := names(ix.Lookup("Handle", "method", 10)); !reflect.DeepEqual(got, []string{"srv/client.go:Client.Handle", "srv/server.go:Server.Handle"}) {
		t.Errorf("method rows = %v", got)
	}
	if got := names(ix.Lookup("Handle", "func", 10)); !reflect.DeepEqual(got, []string{"srv/server.go:handle", "srv/server.go:HandleRequest"}) {
		t.Errorf("func rows = %v", got)
	}
	if got := names(ix.Lookup("Server.Handle", "", 10)); !reflect.DeepEqual(got, []string{"srv/server.go:Server.Handle"}) {
		t.Errorf("receiver query = %v", got)
	}
	if got := ix.Lookup("Handle", "class", 10); len(got) != 0 {
		t.Errorf("class rows = %v", got)
	}
}

func TestBuildBoundsFilesAndSaysSo(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		write(t, root, fmt.Sprintf("f%02d.go", i), fmt.Sprintf("package a\nfunc F%02d() {}\n", i))
	}
	ix := build(t, root, nil, Limits{MaxFiles: 5})
	st := ix.Stats()
	if st.Files != 5 || st.Eligible != 12 || !st.Truncated {
		t.Fatalf("stats = %+v", st)
	}
	if !ix.Has("f00.go") || ix.Has("f11.go") {
		t.Error("the bound did not keep the first files in path order")
	}
}

func TestBuildBoundsDeclarationsPerFile(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package a\n")
	for i := 0; i < MaxPerFile+50; i++ {
		fmt.Fprintf(&b, "func F%d() {}\n", i)
	}
	write(t, root, "big.go", b.String())
	ix := build(t, root, nil, Limits{})
	if out, _ := ix.Outline("big.go"); len(out) != MaxPerFile {
		t.Fatalf("kept %d declarations, want %d", len(out), MaxPerFile)
	}
}

func TestAddKeepsTheTotalInsideItsBound(t *testing.T) {
	ix := &Index{root: "/r", entries: map[string]*Entry{}}
	mk := func(p string, n int) *Entry {
		e := &Entry{Path: p}
		for i := 0; i < n; i++ {
			e.Symbols = append(e.Symbols, Symbol{Name: "A", Line: i + 1})
		}
		return e
	}
	for i := 0; i < 110; i++ {
		ix.add(mk(fmt.Sprintf("f%03d.go", i), MaxPerFile))
	}
	if ix.symbols != MaxTotal || !ix.truncated {
		t.Fatalf("symbols = %d truncated = %v", ix.symbols, ix.truncated)
	}
}

func TestRefreshReparsesOnlyChangedFiles(t *testing.T) {
	root := fixture(t)
	first := build(t, root, nil, Limits{})
	// Rewrite a.go with the same size and mtime: a refresh that trusts the
	// stamp keeps the old row, which proves unchanged files are not re-parsed.
	p := filepath.Join(root, "srv/client.go")
	info, _ := os.Stat(p)
	body, _ := os.ReadFile(p)
	swapped := bytes.ReplaceAll(body, []byte("Client"), []byte("Cliant"))
	if err := os.WriteFile(p, swapped, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// A real change to another file changes its stamp.
	write(t, root, "srv/server.go", "package srv\n\nfunc Renamed() {}\n")
	second := build(t, root, first, Limits{})
	if got := second.Lookup("Cliant", "", 5); len(got) != 0 {
		t.Error("an unchanged stamp was re-parsed")
	}
	if got := second.Lookup("Client", "", 5); len(got) == 0 {
		t.Error("the unchanged entry was lost")
	}
	if len(second.Lookup("Renamed", "", 5)) != 1 || len(second.Lookup("Server", "struct", 5)) != 0 {
		t.Error("a changed file was not re-parsed")
	}
	// A different root never reuses entries.
	other := t.TempDir()
	write(t, other, "srv/client.go", "package srv\nfunc Fresh() {}\n")
	third := build(t, other, first, Limits{})
	if len(third.Lookup("Client", "", 5)) != 0 || len(third.Lookup("Fresh", "", 5)) != 1 {
		t.Error("entries crossed roots")
	}
}

func TestTouchRereadsOrForgetsOneFile(t *testing.T) {
	root := fixture(t)
	ix := build(t, root, nil, Limits{})
	if ix.Touch("srv/server.go") {
		t.Error("an unchanged file reported a change")
	}
	write(t, root, "srv/server.go", "package srv\n\nfunc Added() {}\nfunc Added2() {}\n")
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(root, "srv/server.go"), future, future)
	if !ix.Touch("srv/server.go") {
		t.Error("a changed file reported no change")
	}
	if got, _ := ix.Outline("srv/server.go"); len(got) != 2 || got[0].Name != "Added" {
		t.Errorf("outline after touch = %+v", got)
	}
	if err := os.Remove(filepath.Join(root, "srv/client.go")); err != nil {
		t.Fatal(err)
	}
	before := ix.Stats().Symbols
	if !ix.Touch("srv/client.go") || ix.Has("srv/client.go") || ix.Stats().Symbols >= before {
		t.Error("a removed file stayed in the index")
	}
	if ix.Touch("not/indexed.go") {
		t.Error("touching an unknown file changed the index")
	}
}

func TestBuildPastItsTimeoutIsUnavailableWithNoRows(t *testing.T) {
	root := fixture(t)
	ix, err := Build(context.Background(), root, nil, Limits{Timeout: time.Nanosecond})
	if !errors.Is(err, ErrUnavailable) || ix != nil {
		t.Fatalf("ix = %v, err = %v", ix, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if ix, err := Build(ctx, root, nil, Limits{}); !errors.Is(err, ErrUnavailable) || ix != nil {
		t.Fatalf("cancelled: ix = %v, err = %v", ix, err)
	}
	if _, err := Build(context.Background(), filepath.Join(root, "missing"), nil, Limits{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing root: %v", err)
	}
}

func TestCleanDropsWhatTheIndexDoesNotServe(t *testing.T) {
	for _, s := range []Symbol{
		{Name: "", Line: 1},
		{Name: "a b", Line: 1},
		{Name: "x\x00", Line: 1},
		{Name: "9x", Line: 1},
		{Name: "naïve", Line: 1},
		{Name: strings.Repeat("a", MaxNameLen+1), Line: 1},
		{Name: "ok", Kind: "<system>", Line: 1},
		{Name: "ok", Container: "a.b", Line: 1},
		{Name: "ok", Line: 0},
		{Name: "ok", Line: 5, EndLine: 2},
		{Name: "ok", Line: -1},
	} {
		if _, ok := clean(s); ok {
			t.Errorf("%+v was kept", s)
		}
	}
	if _, ok := clean(Symbol{Name: "_ok9", Kind: "func", Container: "T", Line: 3, EndLine: 3}); !ok {
		t.Error("a good symbol was dropped")
	}
}
