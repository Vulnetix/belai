package sanitize

import (
	"strings"
	"testing"
)

func TestPlainTextDropsTags(t *testing.T) {
	in := "HTTP 403: <html><head><title>403 Forbidden</title></head>\n<body><h1>Forbidden</h1><p>You don&#39;t have access &amp; that is final.</p></body></html>"
	want := "HTTP 403: 403 Forbidden Forbidden You don't have access & that is final."
	if got := PlainText(in, 0); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPlainTextDropsACutOffTag(t *testing.T) {
	// The MCP transport keeps 512 bytes of the body, which can end mid-tag.
	got := PlainText(`HTTP 403: <html><head><link rel="stylesheet" href="/x.css"><link rel="icon" hre`, 0)
	if strings.ContainsAny(got, "<>") || strings.Contains(got, "href") {
		t.Fatalf("markup left in %q", got)
	}
	if got != "HTTP 403:" {
		t.Fatalf("got %q", got)
	}
}

func TestPlainTextDropsScriptStyleAndComments(t *testing.T) {
	in := `a<style>p{color:red}</style>b<SCRIPT type="x">alert(1)</SCRIPT>c<!-- hidden -->d`
	if got := PlainText(in, 0); got != "a b c d" {
		t.Fatalf("got %q", got)
	}
	if got := PlainText(`ok<script>never closed`, 0); got != "ok" {
		t.Fatalf("unclosed script: got %q", got)
	}
}

func TestPlainTextKeepsPlainText(t *testing.T) {
	for _, in := range []string{"connection refused", "a < b and c > d", "x<3", "5 <- 6"} {
		if got := PlainText(in, 0); got != in {
			t.Errorf("%q changed to %q", in, got)
		}
	}
}

func TestPlainTextCapsAndStaysOnOneLine(t *testing.T) {
	got := PlainText("<p>one</p>\n\n<p>two three four</p>", 10)
	if strings.ContainsAny(got, "\n\r") {
		t.Fatalf("not one line: %q", got)
	}
	if r := []rune(got); len(r) != 10 || r[9] != '…' {
		t.Fatalf("got %q", got)
	}
}

func TestPlainTextNonASCIIBeforeScriptClose(t *testing.T) {
	// Byte offsets must survive a rune whose lower case has a different width.
	if got := PlainText("İ<script>x</script>y", 0); got != "İ y" {
		t.Fatalf("got %q", got)
	}
}
