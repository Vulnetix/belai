package netguard

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestCheckURLFetchAccepts(t *testing.T) {
	ok := map[string]string{
		"https://example.com/a/b?q=1":         "https://example.com/a/b?q=1",
		"HTTP://Example.COM:8080/x":           "http://example.com:8080/x",
		"https://example.com./":               "https://example.com/",
		"https://sub.example.co.uk/p%20q":     "https://sub.example.co.uk/p%20q",
		"https://[2606:4700:4700::1111]/":     "https://[2606:4700:4700::1111]/",
		"https://93.184.216.34/":              "https://93.184.216.34/",
		"https://example.com/日本語":             "https://example.com/%E6%97%A5%E6%9C%AC%E8%AA%9E",
		"https://my_host.example.com/":        "https://my_host.example.com/",
		"https://xn--nxasmq6b.example.com/":   "https://xn--nxasmq6b.example.com/",
		"https://example.com/a?next=%2Fother": "https://example.com/a?next=%2Fother",
	}
	for in, want := range ok {
		u, err := CheckURL(in, Fetch)
		if err != nil {
			t.Errorf("CheckURL(%q) = %v", in, err)
			continue
		}
		got := u.String()
		if strings.HasSuffix(in, "example.com./") {
			want = "https://example.com/"
		}
		if got != want {
			t.Errorf("CheckURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckURLFetchRejects(t *testing.T) {
	bad := []string{
		"", "example.com", "ftp://example.com/", "file:///etc/passwd", "gopher://example.com/",
		"javascript:alert(1)", "https://user:pw@example.com/", "https://user@example.com/",
		"https://example.com/a b", "https://example.com/a\tb", "https://example.com/a\nb",
		"https://example.com/a\\b", "https://exa\x00mple.com/",
		"https://example.com/%0d%0aSet-Cookie:x", "https://example.com/%250d%250a", "https://example.com/%2500",
		"https://example.com/?a=%0a", "https://example.com/%zz",
		"http://localhost/", "http://LOCALHOST./", "http://a.localhost/", "http://printer.local/", "http://db.internal/",
		"http://intranet/", "http://metadata.google.internal/", "http://host.lan/",
		"http://127.0.0.1/", "http://127.1/", "http://2130706433/", "http://0x7f000001/", "http://0x7f.0.0.1/",
		"http://0177.0.0.1/", "http://017700000001/", "http://10.0.0.1/", "http://192.168.1.1/",
		"http://172.16.0.1/", "http://172.31.255.255/", "http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/", "http://0.0.0.0/", "http://0/", "http://224.0.0.1/", "http://255.255.255.255/",
		"http://192.0.0.192/", "http://198.18.0.1/",
		"http://[::1]/", "http://[::]/", "http://[::ffff:127.0.0.1]/", "http://[::ffff:7f00:1]/",
		"http://[::ffff:10.0.0.1]/", "http://[fe80::1]/", "http://[fe80::1%25eth0]/", "http://[fc00::1]/",
		"http://[fd00:ec2::254]/", "http://[64:ff9b::7f00:1]/", "http://[2002:7f00:1::]/", "http://[2001:0:4136:e378::1]/",
		"https://example.com:0/", "https://example.com:123456/",
		"https://" + strings.Repeat("a", 300) + ".com/",
		"https://example.com/" + strings.Repeat("a", MaxURLBytes),
		"https://exa mple.com/",
	}
	for _, in := range bad {
		if _, err := CheckURL(in, Fetch); !errors.Is(err, ErrURL) {
			t.Errorf("CheckURL(%q) = %v, want rejection", in, err)
		}
	}
}

func TestCheckURLEndpoint(t *testing.T) {
	ok := []string{
		"https://jev.example.com", "https://10.0.0.5:8443/base", "http://localhost:8080", "http://127.0.0.1:9000/x",
		"http://[::1]:8000", "https://jev/", "http://sub.localhost/", "https://internal.corp.local/v1",
	}
	for _, in := range ok {
		if _, err := CheckURL(in, Endpoint); err != nil {
			t.Errorf("Endpoint %q = %v", in, err)
		}
	}
	bad := []string{
		"http://example.com", "http://10.0.0.5", "ftp://x.com", "https://u:p@example.com", "https://ex ample.com",
		"http://0x7f.1/", "http://2130706433/", "https://example.com/%0d%0a", "", "https:///path",
	}
	for _, in := range bad {
		if _, err := CheckURL(in, Endpoint); !errors.Is(err, ErrURL) {
			t.Errorf("Endpoint %q = %v, want rejection", in, err)
		}
	}
}

func TestIDNAHost(t *testing.T) {
	u, err := CheckURL("https://bücher.example/", Fetch)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "xn--bcher-kva.example" {
		t.Fatalf("host = %q", u.Host)
	}
	if _, err := CheckURL("https://exa"+string(rune(0x200b))+"mple.com/", Fetch); err == nil {
		t.Fatal("zero width space in host accepted")
	}
}

func TestForbidden(t *testing.T) {
	forbid := []string{
		"0.0.0.1", "10.1.2.3", "100.64.0.1", "100.127.255.255", "127.0.0.1", "127.255.255.254", "169.254.169.254",
		"172.16.0.1", "172.31.0.1", "192.0.0.1", "192.168.0.1", "198.18.0.1", "198.19.255.255", "224.0.0.1",
		"239.255.255.255", "240.0.0.1", "255.255.255.255", "::", "::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"::ffff:169.254.169.254", "64:ff9b::1", "100::1", "2001::1", "2001:db8::1", "2002::1", "fc00::1", "fd12::1",
		"fd00:ec2::254", "fe80::1", "fec0::1", "ff02::1",
	}
	for _, s := range forbid {
		if !Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s should be forbidden", s)
		}
	}
	allow := []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "172.32.0.1", "100.63.255.255", "100.128.0.1", "192.0.3.1",
		"198.20.0.1", "2606:4700:4700::1111", "2a00:1450:4001:81a::200e", "::ffff:8.8.8.8"}
	for _, s := range allow {
		if Forbidden(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
	if !Forbidden(netip.Addr{}) || !ForbiddenIP(nil) || !ForbiddenIP(net.IP{1, 2, 3}) {
		t.Error("zero or malformed addresses must be forbidden")
	}
	if !ForbiddenIP(net.ParseIP("127.0.0.1")) || ForbiddenIP(net.ParseIP("8.8.8.8")) {
		t.Error("ForbiddenIP disagrees with Forbidden")
	}
	if !Forbidden(netip.MustParseAddr("fe80::1%eth0")) {
		t.Error("zoned link-local should be forbidden")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST", "localhost.", "a.localhost", "127.0.0.1", "127.9.9.9", "::1", "[::1]", "::ffff:127.0.0.1"} {
		if !IsLoopbackHost(h) {
			t.Errorf("%q should be loopback", h)
		}
	}
	for _, h := range []string{"", "example.com", "10.0.0.1", "localhost.evil.com", "notlocalhost", "0.0.0.0", "::"} {
		if IsLoopbackHost(h) {
			t.Errorf("%q should not be loopback", h)
		}
	}
}

func FuzzCheckURL(f *testing.F) {
	for _, s := range []string{
		"https://example.com/", "http://127.1/", "http://[::ffff:7f00:1]/", "https://a@b/", "http://%31%32%37.0.0.1/",
		"https://example.com/%0d%0a", "http://0x7f.1/", "http://２１３０７０６４３３/", "https://exa%6Dple.com/",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := CheckURL(s, Fetch)
		if err != nil {
			return
		}
		if u.User != nil {
			t.Fatalf("credentials accepted: %q", s)
		}
		host := strings.Trim(u.Hostname(), "[]")
		if a, err := netip.ParseAddr(host); err == nil && Forbidden(a) {
			t.Fatalf("forbidden address %s accepted from %q", host, s)
		}
		if IsLoopbackHost(host) || internalName(host) {
			t.Fatalf("internal host %q accepted from %q", host, s)
		}
		if numericLike(host) {
			t.Fatalf("non-canonical numeric host %q accepted from %q", host, s)
		}
		for i := 0; i < len(u.String()); i++ {
			if c := u.String()[i]; c <= ' ' || c == 0x7f || c == '\\' {
				t.Fatalf("control byte in accepted URL %q", u.String())
			}
		}
	})
}
