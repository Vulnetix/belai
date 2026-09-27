package e2e

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestFirewallHeadless routes a headless run through a custom header-mode
// firewall. It checks four things. The firewall key goes in its own header and
// the provider key is not sent. Response events print one line each to
// stderr, and stdout stays the reply. A guardrail refusal fails the run with
// a card line. Nothing the firewall said reaches a model turn.
func TestFirewallHeadless(t *testing.T) {
	var mu sync.Mutex
	var gwKeys, providerAuth, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		body := buf.String()
		mu.Lock()
		gwKeys = append(gwKeys, r.Header.Get("X-Gw-Key"))
		providerAuth = append(providerAuth, r.Header.Get("Authorization"))
		bodies = append(bodies, body)
		mu.Unlock()
		if strings.Contains(body, "BLOCKME") && !strings.Contains(body, "security classifier") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"request blocked by AI firewall policy: no secrets","type":"policy_violation","code":"request_blocked","blocked_by":"No secrets","violations":[{"policy_name":"No secrets","action":"block"}]}}`))
			return
		}
		if !strings.Contains(body, "security classifier") && !strings.Contains(body, "operating-mode classifier") {
			w.Header().Set("X-Vulnetix-Firewall-Decision", "redact")
			w.Header().Set("X-Vulnetix-Firewall-Rules", "PII%20emails")
			w.Header().Set("X-Vulnetix-Firewall-Redactions", "1")
			w.Header().Set("X-Vulnetix-Firewall-Request-Id", "req-e2e-1")
		}
		if strings.Contains(body, "security classifier") {
			writeChat(w, "SAFE")
			return
		}
		writeChat(w, "mock reply")
	}))
	defer srv.Close()

	global := `{"firewall":{"enabled":true,"active":"corp","instances":{"corp":{"adapter":"custom","url":"https://gw.example/v1","mode":"header","header":"X-Gw-Key"}}}}`
	run := func(prompt string) (string, string, int) {
		dir := t.TempDir()
		home := filepath.Join(t.TempDir(), "belai-home")
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte(global), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, errb bytes.Buffer
		cmd := exec.Command(belaiBin, "-trust-dir", "-tools=false", "-provider", "openai", "-model", "test", "-prompt", prompt)
		cmd.Dir = dir
		// BELAI_BASE_URL points the route at the mock; the route's headers
		// still apply.
		cmd.Env = append(os.Environ(), "BELAI_BASE_URL="+srv.URL, "OPENAI_API_KEY=sk-provider-secret",
			"BELAI_FIREWALL_CORP_API_KEY=fw-secret", "BELAI_HOME="+home)
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := 0
		if err := cmd.Run(); err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run belai: %v", err)
			}
			code = ee.ExitCode()
		}
		return out.String(), errb.String(), code
	}

	out, errOut, code := run("what model is this")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if strings.TrimSpace(out) != "mock reply" {
		t.Fatalf("stdout = %q, want only the reply", out)
	}
	if !strings.Contains(errOut, "firewall: Custom firewall redacted") || !strings.Contains(errOut, "rules=PII emails") || !strings.Contains(errOut, "request=req-e2e-1") {
		t.Fatalf("stderr = %q, want a redaction line", errOut)
	}
	mu.Lock()
	for i := range gwKeys {
		if gwKeys[i] != "fw-secret" {
			t.Fatalf("request %d carried firewall key %q (auth %q) body %.300s", i, gwKeys[i], providerAuth[i], bodies[i])
		}
		if strings.Contains(providerAuth[i], "sk-provider-secret") {
			t.Fatalf("request %d sent the provider key to a BYOK firewall", i)
		}
	}
	for _, b := range bodies {
		if strings.Contains(b, "req-e2e-1") || strings.Contains(b, "PII emails") {
			t.Fatal("firewall response text reached a model request")
		}
	}
	mu.Unlock()

	_, errOut, code = run("please BLOCKME now")
	if code == 0 {
		t.Fatal("a blocked request exited 0")
	}
	if !strings.Contains(errOut, "firewall: Custom firewall blocked") || !strings.Contains(errOut, "code=request_blocked") || !strings.Contains(errOut, "rules=No secrets") {
		t.Fatalf("stderr = %q, want a block line", errOut)
	}
}
