package tools

import "testing"

// TestAllowedContentType pins the content-type gate: only text/* and
// JSON/XHTML bodies are returned; binary and empty types are refused.
func TestAllowedContentType(t *testing.T) {
	allow := []string{
		"text/html",
		"text/plain",
		"text/html; charset=utf-8",
		"TEXT/HTML", // case-insensitive
		"application/json",
		"application/json; charset=utf-8",
		"application/xhtml+xml",
	}
	for _, ct := range allow {
		if !allowedContentType(ct) {
			t.Errorf("allowedContentType(%q) = false, want true", ct)
		}
	}
	deny := []string{
		"",
		"application/octet-stream",
		"application/pdf",
		"image/png",
		"binary",
	}
	for _, ct := range deny {
		if allowedContentType(ct) {
			t.Errorf("allowedContentType(%q) = true, want false", ct)
		}
	}
}

// TestHTMLText pins the minimal best-effort tag stripper.
func TestHTMLText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>Hello</p>", "Hello"},
		{"<p>a<b>c</b>d</p>", "acd"},
		{"no tags", "no tags"},
		{"", ""},
		{"  <div>x</div>  ", "x"},
		{"<script>evil()</script>safe", "evil()safe"},
	}
	for _, c := range cases {
		if got := htmlText(c.in); got != c.want {
			t.Errorf("htmlText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestValidateHostLiteralIP covers the pre-dial SSRF guard's deterministic
// literal-IP path (no DNS round-trip): loopback and private addresses are
// rejected, a public literal passes.
func TestValidateHostLiteralIP(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "0.0.0.0"} {
		if err := validateHost(host); err == nil {
			t.Errorf("validateHost(%q) = nil, want a private-address rejection", host)
		}
	}
	for _, host := range []string{"8.8.8.8", "93.184.216.34"} {
		if err := validateHost(host); err != nil {
			t.Errorf("validateHost(%q) = %v, want nil", host, err)
		}
	}
}

// TestWebFetchSubjectEmpty covers the non-string / missing url branch.
func TestWebFetchSubjectEmpty(t *testing.T) {
	wf := &WebFetch{}
	if got := wf.Subject(map[string]any{"url": 42}); got != "" {
		t.Fatalf("Subject(non-string url) = %q, want empty", got)
	}
	if got := wf.Subject(map[string]any{}); got != "" {
		t.Fatalf("Subject(missing url) = %q, want empty", got)
	}
}
