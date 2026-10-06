//go:build belai_sandbox

package provider

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/wire"
)

func TestSandboxBuiltinDescriptor(t *testing.T) {
	d, ok := Lookup("builtin")
	if !ok || !Builtin("builtin") {
		t.Fatal("builtin is not registered")
	}
	if d.Surface != wire.SurfaceWorkersAI || d.Auth != AuthBearer || !d.Usage {
		t.Fatalf("descriptor = %+v", d)
	}
	if d.DefaultModel != "pix-smart" || d.FastModel != "pix-fast" || d.ListPath != "/ai/models/search" {
		t.Fatalf("models/list = %q %q %q", d.DefaultModel, d.FastModel, d.ListPath)
	}
	if len(d.Models) != 2 || d.Models[0].ID != "pix-smart" || d.Models[0].Label != "Pix Smart" || d.Models[1].ID != "pix-fast" || d.Models[1].Label != "Pix Fast" {
		t.Fatalf("models = %+v", d.Models)
	}
	stock := registry["cloudflare-workers-ai"]
	if len(d.Fields) != len(stock.Fields) {
		t.Fatalf("fields = %+v", d.Fields)
	}
	for i, f := range d.Fields {
		if f.Name != stock.Fields[i].Name || strings.Join(f.EnvVars, ",") != strings.Join(stock.Fields[i].EnvVars, ",") || f.Secret != stock.Fields[i].Secret {
			t.Fatalf("field %d = %+v, stock %+v", i, f, stock.Fields[i])
		}
	}
	if got := d.BaseURLBuilder(map[string]string{"account_id": "acct"}); got != "https://api.cloudflare.com/client/v4/accounts/acct" {
		t.Fatalf("base URL = %q", got)
	}
}

func TestSandboxBuiltinRequestShape(t *testing.T) {
	p, err := New("builtin", "https://api.cloudflare.com/client/v4/accounts/acct", "k")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req, err := p.NewWorkersAIRequest("pix-smart", wire.WorkersAIRequest{
		Messages: []wire.OpenAIChatMessage{{Role: "user", Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("NewWorkersAIRequest: %v", err)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %s", req.Method)
	}
	if got, want := req.URL.String(), "https://api.cloudflare.com/client/v4/accounts/acct/ai/run/pix-smart"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer k" {
		t.Fatalf("Authorization = %q", got)
	}
}
