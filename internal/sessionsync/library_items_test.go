package sessionsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/libitem"
)

type itemSite struct {
	t       *testing.T
	method  string
	path    string
	query   string
	body    map[string]json.RawMessage
	status  int
	respond string
}

func (s *itemSite) start() (*Client, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method, s.path, s.query = r.Method, strings.TrimPrefix(r.URL.Path, apiPath), r.URL.RawQuery
		s.body = nil
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &s.body)
		}
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		w.Write([]byte(s.respond))
	}))
	c, err := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		s.t.Fatal(err)
	}
	return c, srv.Close
}

func TestItemBackupSendsAMarkdownDocumentAsAString(t *testing.T) {
	s := &itemSite{t: t, respond: `{"item":{"id":"i1"},"version":"202610011234","created":true}`}
	c, stop := s.start()
	defer stop()
	got, err := c.ItemBackup(context.Background(), testHost, "d1", libitem.Skill, []byte("---\nname: a\n---\n\nbody\n"))
	if err != nil || got.Version != "202610011234" || !got.Created {
		t.Fatalf("got %+v, %v", got, err)
	}
	if s.method != "POST" || s.path != "/hosts/"+testHost+"/library/item-backups" {
		t.Errorf("%s %s", s.method, s.path)
	}
	var body string
	_ = json.Unmarshal(s.body["body"], &body)
	if string(s.body["dispatch"]) != `"d1"` || string(s.body["kind"]) != `"skill"` || body != "---\nname: a\n---\n\nbody\n" {
		t.Errorf("body = %s", s.body)
	}
}

func TestItemBackupSendsAJSONDocumentAsAnObject(t *testing.T) {
	s := &itemSite{t: t, respond: `{"version":{"version":"202610011234","kind":"backup"},"created":false}`}
	c, stop := s.start()
	defer stop()
	got, err := c.ItemBackup(context.Background(), testHost, "d1", libitem.Process, []byte(`{"command":"x","name":"a"}`+"\n"))
	if err != nil || got.Version != "202610011234" || got.Created {
		t.Fatalf("got %+v, %v: a version sent as an object is read too", got, err)
	}
	if string(s.body["kind"]) != `"process"` || !strings.HasPrefix(string(s.body["body"]), `{"command":"x"`) {
		t.Errorf("body = %s", s.body)
	}
}

func TestItemBackupBoundsTheDocument(t *testing.T) {
	s := &itemSite{t: t, respond: `{}`}
	c, stop := s.start()
	defer stop()
	for kind, max := range map[libitem.Kind]int{libitem.Skill: 32 << 10, libitem.Prompt: 32 << 10} {
		if _, err := c.ItemBackup(context.Background(), testHost, "d", kind, []byte(strings.Repeat("x", max+1))); err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Errorf("%s over its limit: %v", kind, err)
		}
		if _, err := c.ItemBackup(context.Background(), testHost, "d", kind, []byte(strings.Repeat("x", max))); err != nil {
			t.Errorf("%s at its limit: %v", kind, err)
		}
	}
	if _, err := c.ItemBackup(context.Background(), testHost, "d", "nope", []byte("x")); err == nil {
		t.Error("an unknown kind was sent")
	}
}

func TestItemFetchReadsADocument(t *testing.T) {
	s := &itemSite{t: t, respond: `{"version":"202610011234","name":"release","overwrite":true,"body":"---\nname: release\n---\n\nx\n"}`}
	c, stop := s.start()
	defer stop()
	got, err := c.ItemFetch(context.Background(), testHost, libitem.Skill, "11111111-1111-4111-8111-111111111111", "202610011234", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != "---\nname: release\n---\n\nx\n" || got.Name != "release" || !got.Overwrite || got.Version != "202610011234" {
		t.Fatalf("got %+v", got)
	}
	if s.method != "GET" || s.path != "/hosts/"+testHost+"/library/items/skills/11111111-1111-4111-8111-111111111111/versions/202610011234" || s.query != "dispatch=d1" {
		t.Errorf("%s %s ?%s", s.method, s.path, s.query)
	}
}

func TestItemFetchRefusesWhatItCannotUse(t *testing.T) {
	cases := []struct {
		name    string
		kind    libitem.Kind
		respond string
		want    string
	}{
		{"no body", libitem.Skill, `{"name":"a"}`, "no document"},
		{"null body", libitem.Skill, `{"body":null}`, "no document"},
		{"a markdown body that is an object", libitem.Skill, `{"body":{"a":1}}`, "not text"},
		{"a markdown body over twice its limit", libitem.Skill, `{"body":"` + strings.Repeat("x", 64<<10+1) + `"}`, "larger than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &itemSite{t: t, respond: c.respond}
			cl, stop := s.start()
			defer stop()
			_, err := cl.ItemFetch(context.Background(), testHost, c.kind, "i", "202610011234", "d")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
		})
	}
	s := &itemSite{t: t, respond: `{}`}
	c, stop := s.start()
	defer stop()
	if _, err := c.ItemFetch(context.Background(), testHost, "nope", "i", "v", "d"); err == nil {
		t.Error("an unknown kind was fetched")
	}
}

func TestItemRoutesMapStatusCodes(t *testing.T) {
	for status, want := range map[int]error{404: ErrNotFound, 401: ErrUnauthorized, 409: ErrConflict} {
		s := &itemSite{t: t, status: status, respond: `{"error":"x"}`}
		c, stop := s.start()
		_, err := c.ItemFetch(context.Background(), testHost, libitem.Skill, "i", "v", "d")
		if !errors.Is(err, want) {
			t.Errorf("fetch %d: %v", status, err)
		}
		_, err = c.ItemBackup(context.Background(), testHost, "d", libitem.Skill, []byte("x"))
		if !errors.Is(err, want) {
			t.Errorf("backup %d: %v", status, err)
		}
		_, err = c.LibrarySyncItem(context.Background(), testHost, libitem.Skill, "a", []byte("x"))
		if !errors.Is(err, want) {
			t.Errorf("sync item %d: %v", status, err)
		}
		stop()
	}
}

func TestLibrarySyncAllCarriesItemsAndReadsTheirAnswers(t *testing.T) {
	s := &itemSite{t: t, respond: `{"agents":[{"id":"a1","action":"push"}],"crews":[],"items":[{"kind":"skill","name":"release","action":"diverged","version":"202610011234"},{"kind":"prompt","name":"deploy","action":"push"}]}`}
	c, stop := s.start()
	defer stop()
	ag, cr, it, err := c.LibrarySyncAll(context.Background(), testHost,
		[]SyncItem{{ID: "a1", SHA256: "aa"}}, nil,
		[]SyncItemRef{{Kind: "skill", Name: "release", SHA256: "bb"}, {Kind: "prompt", Name: "deploy", SHA256: "cc"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ag) != 1 || len(cr) != 0 || len(it) != 2 || it[0].Action != SyncDiverged || it[0].Version != "202610011234" || it[1].Action != SyncPush {
		t.Fatalf("answers: %+v %+v %+v", ag, cr, it)
	}
	if s.method != "POST" || s.path != "/hosts/"+testHost+"/library/sync" {
		t.Errorf("%s %s", s.method, s.path)
	}
	var items []SyncItemRef
	_ = json.Unmarshal(s.body["items"], &items)
	if len(items) != 2 || items[0] != (SyncItemRef{Kind: "skill", Name: "release", SHA256: "bb"}) {
		t.Errorf("items = %s", s.body["items"])
	}
}

// A host with no items sends none: the request is the one an older server knows.
func TestLibrarySyncAllWithoutItemsSendsNoItemsKey(t *testing.T) {
	s := &itemSite{t: t, respond: `{"agents":[],"crews":[]}`}
	c, stop := s.start()
	defer stop()
	if _, _, it, err := c.LibrarySyncAll(context.Background(), testHost, nil, nil, nil); err != nil || len(it) != 0 {
		t.Fatalf("%v %v", it, err)
	}
	if _, ok := s.body["items"]; ok {
		t.Errorf("an items key was sent: %s", s.body["items"])
	}
	// The old call is unchanged.
	if _, _, err := c.LibrarySync(context.Background(), testHost, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestLibrarySyncAllBoundsItems(t *testing.T) {
	s := &itemSite{t: t, respond: `{}`}
	c, stop := s.start()
	defer stop()
	ok := make([]SyncItemRef, MaxSyncItems)
	if _, _, _, err := c.LibrarySyncAll(context.Background(), testHost, nil, nil, ok); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	if _, _, _, err := c.LibrarySyncAll(context.Background(), testHost, nil, nil, append(ok, SyncItemRef{})); err == nil {
		t.Fatal("over the limit was sent")
	}
}

func TestLibrarySyncItemPushesOneDocument(t *testing.T) {
	s := &itemSite{t: t, respond: `{"version":"202610011234"}`}
	c, stop := s.start()
	defer stop()
	v, err := c.LibrarySyncItem(context.Background(), testHost, libitem.Prompt, "deploy", []byte("---\nname: deploy\n---\n\nx\n"))
	if err != nil || v != "202610011234" {
		t.Fatalf("%q %v", v, err)
	}
	if s.method != "PUT" || s.path != "/hosts/"+testHost+"/library/sync/items" {
		t.Errorf("%s %s", s.method, s.path)
	}
	if string(s.body["kind"]) != `"prompt"` || string(s.body["name"]) != `"deploy"` {
		t.Errorf("body = %s", s.body)
	}
	if _, err := c.LibrarySyncItem(context.Background(), testHost, libitem.Skill, "a", []byte(strings.Repeat("x", 32<<10+1))); err == nil {
		t.Error("an oversized document was pushed")
	}
}

func TestRCInfoCarriesItems(t *testing.T) {
	b, _ := json.Marshal(RCInfo{Items: []RCItem{{Kind: "skill", Name: "a", SHA256: "ff"}}})
	if !strings.Contains(string(b), `"items":[{"kind":"skill","name":"a","sha256":"ff"}]`) {
		t.Fatalf("RCInfo = %s", b)
	}
	var d Dispatch
	if err := json.Unmarshal([]byte(`{"id":"d","kind":"item_install","itemKind":"skill","name":"a","library":"u","version":"202610011234","overwrite":true}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Kind != "item_install" || d.ItemKind != "skill" || d.Name != "a" || !d.Overwrite {
		t.Fatalf("dispatch = %+v", d)
	}
}
