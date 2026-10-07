package sessionsync

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type scanSite struct {
	path, enc string
	body      map[string]json.RawMessage
	status    int
}

func (s *scanSite) client(t *testing.T) (*Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path, s.enc = strings.TrimPrefix(r.URL.Path, apiPath), r.Header.Get("Content-Encoding")
		var rd io.Reader = r.Body
		if s.enc == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "bad gzip", 400)
				return
			}
			rd = zr
		}
		b, _ := io.ReadAll(rd)
		s.body = nil
		_ = json.Unmarshal(b, &s.body)
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		w.Write([]byte(`{"item":{"id":"i1"},"version":"202610011234","created":true}`))
	}))
	c, err := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, srv.Close
}

func TestPostLibraryScanSendsTheReportAndGzipsAFatOne(t *testing.T) {
	s := &scanSite{}
	c, stop := s.client(t)
	defer stop()
	small := map[string]any{"scannedAt": 1, "items": []string{}}
	if err := c.PostLibraryScan(context.Background(), testHost, "d1", small); err != nil {
		t.Fatal(err)
	}
	if s.path != "/hosts/"+testHost+"/library/scans" || s.enc != "" || string(s.body["dispatch"]) != `"d1"` {
		t.Fatalf("%s enc=%q body=%v", s.path, s.enc, s.body)
	}
	var rep map[string]any
	if err := json.Unmarshal(s.body["report"], &rep); err != nil || rep["scannedAt"] != float64(1) {
		t.Fatalf("report = %s", s.body["report"])
	}
	big := map[string]any{"items": []string{strings.Repeat("x", 20<<10)}}
	if err := c.PostLibraryScan(context.Background(), testHost, "d2", big); err != nil {
		t.Fatal(err)
	}
	if s.enc != "gzip" || string(s.body["dispatch"]) != `"d2"` {
		t.Fatalf("a 20 KiB report: enc=%q body=%v", s.enc, s.body)
	}
	s.status = http.StatusNotFound
	if err := c.PostLibraryScan(context.Background(), testHost, "d3", small); !errors.Is(err, ErrNotFound) {
		t.Errorf("an undelivered request: %v", err)
	}
	s.status = http.StatusConflict
	if err := c.PostLibraryScan(context.Background(), testHost, "d3", small); !errors.Is(err, ErrConflict) {
		t.Errorf("a replay: %v", err)
	}
}

func TestPostLibraryImportBodyShapes(t *testing.T) {
	s := &scanSite{}
	c, stop := s.client(t)
	defer stop()
	md, err := ImportBody(true, []byte("---\nname: a\n---\n\nbody\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.PostLibraryImport(context.Background(), testHost, LibraryImport{Dispatch: "d1", Kind: "command", Name: "a", Body: md})
	if err != nil || got.Version != "202610011234" || !got.Created {
		t.Fatalf("%+v %v", got, err)
	}
	if s.path != "/hosts/"+testHost+"/library/imports" {
		t.Errorf("path %s", s.path)
	}
	var text string
	if json.Unmarshal(s.body["body"], &text) != nil || !strings.HasPrefix(text, "---") {
		t.Errorf("a Markdown kind is a string: %s", s.body["body"])
	}
	if _, has := s.body["skills"]; has {
		t.Error("skills sent for a command")
	}
	obj, err := ImportBody(false, []byte(`{"name":"b"}`))
	if err != nil {
		t.Fatal(err)
	}
	skills := []LibraryImportSkill{{Name: "lint", Body: "---\nname: lint\n---\n\nx\n"}}
	if _, err := c.PostLibraryImport(context.Background(), testHost, LibraryImport{Dispatch: "d2", Kind: "crew", Name: "b", Body: obj, Skills: skills, Target: "reviewer"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(s.body["body"]), "{") || string(s.body["target"]) != `"reviewer"` || !strings.Contains(string(s.body["skills"]), `"lint"`) {
		t.Errorf("body = %v", s.body)
	}
	// A hook carries its scripts: path and base64 content, no other field.
	hook, err := ImportBody(false, []byte(`{"name":"claude-code-user","hooks":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	files := []ImportFile{{Path: "guard.sh", Content: "ZXhpdCAwCg=="}}
	if _, err := c.PostLibraryImport(context.Background(), testHost, LibraryImport{Dispatch: "d3", Kind: "hook", Name: "claude-code-user", Body: hook, Files: files}); err != nil {
		t.Fatal(err)
	}
	if string(s.body["files"]) != `[{"path":"guard.sh","content":"ZXhpdCAwCg=="}]` || string(s.body["kind"]) != `"hook"` {
		t.Errorf("hook upload = %v", s.body)
	}
	for _, bad := range []struct {
		text bool
		doc  string
	}{{true, ""}, {false, "not json"}, {true, strings.Repeat("x", MaxImportBytes+1)}} {
		if _, err := ImportBody(bad.text, []byte(bad.doc)); err == nil {
			t.Errorf("ImportBody accepted %.20q", bad.doc)
		}
	}
}
