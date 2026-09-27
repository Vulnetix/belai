package kiromodels

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseWalksSchemas(t *testing.T) {
	page := `{"models":[
	  {"modelId":"claude-opus-4.5","modelName":"Claude‮ Opus","supportedInputTypes":["TEXT","IMAGE"],
	   "tokenLimits":{"maxInputTokens":200000,"maxOutputTokens":32000},
	   "whatever":{"deeper":{"properties":{"output_config":{"properties":{"effort":{"enum":["low","medium","high","bad value!"]}}},
	                                      "max_tokens":{"minimum":1024,"maximum":32000}}}}},
	  {"modelId":"gpt-5.6","requestSchema":"{\"properties\":{\"reasoning\":{\"properties\":{\"effort\":{\"enum\":[\"low\",\"high\"]}}}}}"},
	  {"modelId":"plain"},
	  {"modelId":"bad id with spaces"}
	],"nextToken":"n2"}`
	infos, next, err := Parse([]byte(page))
	if err != nil || next != "n2" || len(infos) != 3 {
		t.Fatalf("Parse = %+v %q %v", infos, next, err)
	}
	o := infos[0]
	if !o.Images || o.MaxInput != 200000 || o.EffortBranch != BranchOutputConfig || len(o.Efforts) != 3 || o.MaxTokensMax != 32000 || o.Name != "Claude Opus" {
		t.Fatalf("opus = %+v", o)
	}
	if g := infos[1]; g.EffortBranch != BranchReasoning || len(g.Efforts) != 2 || g.Images {
		t.Fatalf("gpt (schema as text) = %+v", g)
	}
	if p := infos[2]; p.EffortBranch != "" || p.RequestFields("high", 1000) != nil {
		t.Fatalf("plain = %+v", p)
	}
	f := o.RequestFields("medium", 50)
	if f["output_config"].(map[string]any)["effort"] != "medium" || f["max_tokens"] != 1024 {
		t.Fatalf("fields = %v", f)
	}
	if f := infos[1].RequestFields("medium", 0); f != nil {
		t.Fatalf("undeclared effort sent: %v", f)
	}
}

func TestFetchPaginatesAndPins(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("nextToken") == "" {
			fmt.Fprint(w, `{"models":[{"modelId":"a"}],"nextToken":"p2"}`)
			return
		}
		fmt.Fprint(w, `{"models":[{"modelId":"b"}]}`)
	}))
	defer srv.Close()
	infos, err := Fetch(context.Background(), srv.Client(), srv.URL, "tok", "")
	if err != nil || len(infos) != 2 || calls != 2 {
		t.Fatalf("Fetch = %+v, %v (calls %d)", infos, err, calls)
	}
	if _, err := Fetch(context.Background(), nil, "https://evil.example", "tok", ""); err == nil {
		t.Fatal("unpinned host accepted")
	}
}

func TestCacheTTL(t *testing.T) {
	Forget()
	defer Forget()
	clock := time.Unix(0, 0)
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()

	if _, _, fresh := Lookup("https://q", "a"); fresh {
		t.Fatal("empty cache is fresh")
	}
	Remember("https://q/", []Info{{ID: "a", MaxInput: 5}})
	if i, found, fresh := Lookup("https://q", "a"); !found || !fresh || i.MaxInput != 5 {
		t.Fatal("remembered model not found")
	}
	if _, found, fresh := Lookup("https://q", "zzz"); found || !fresh {
		t.Fatal("unknown model in a fresh catalogue")
	}
	clock = clock.Add(TTL + time.Second)
	if _, _, fresh := Lookup("https://q", "a"); fresh {
		t.Fatal("stale catalogue reported fresh")
	}
}
