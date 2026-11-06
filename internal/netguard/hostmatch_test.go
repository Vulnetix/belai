package netguard

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckHostPort(t *testing.T) {
	ok := []struct {
		host string
		port int
		want string
	}{
		{"example.com", 443, "example.com"},
		{"Example.COM.", 80, "example.com"},
		{"api.example.com", 443, "api.example.com"},
		{"bücher.example", 443, "xn--bcher-kva.example"},
		{"93.184.216.34", 443, "93.184.216.34"},
		{"[2606:4700::1111]", 443, "2606:4700::1111"},
		{"2606:4700::1111", 443, "2606:4700::1111"},
	}
	for _, c := range ok {
		got, err := CheckHostPort(c.host, c.port)
		if err != nil || got != c.want {
			t.Errorf("CheckHostPort(%q, %d) = %q, %v; want %q", c.host, c.port, got, err, c.want)
		}
	}
	bad := []struct {
		host string
		port int
	}{
		{"example.com", 22}, {"example.com", 0}, {"example.com", 8080}, {"example.com", -1}, {"example.com", 65536},
		{"", 443}, {".", 443}, {"localhost", 443}, {"db.internal", 443}, {"printer.local", 443}, {"intranet", 443},
		{"127.0.0.1", 443}, {"10.0.0.5", 443}, {"169.254.169.254", 80}, {"[::1]", 443}, {"fd00:ec2::254", 80},
		{"::ffff:127.0.0.1", 443},
		{"2130706433", 443}, {"0x7f.1", 443}, {"127.1", 80}, {"0177.0.0.1", 80},
		{"exa mple.com", 443}, {"exa\nmple.com", 443}, {"exa\x00mple.com", 443}, {"exam%70le.com", 443},
		{"example.com/path", 443}, {"user@example.com", 443}, {"exa\\mple.com", 443},
		{"fe80::1%eth0", 443}, {strings.Repeat("a", 254), 443}, {"a​b.example", 443},
	}
	for _, c := range bad {
		if got, err := CheckHostPort(c.host, c.port); err == nil {
			t.Errorf("CheckHostPort(%q, %d) = %q, want a refusal", c.host, c.port, got)
		} else if !errors.Is(err, ErrURL) {
			t.Errorf("CheckHostPort(%q, %d) error %v does not wrap ErrURL", c.host, c.port, err)
		}
	}
}

func FuzzCheckHostPort(f *testing.F) {
	for _, s := range []string{"example.com", "[::1]", "0x7f.1", "a b", "bücher.example", "..", "a..b", "-", "a.b.c.d.e", "1.2.3.4", "::ffff:1.2.3.4"} {
		f.Add(s, 443)
	}
	f.Fuzz(func(t *testing.T, host string, port int) {
		got, err := CheckHostPort(host, port)
		if err != nil {
			return
		}
		// What is accepted is canonical, matches the port rule, and is stable:
		// checking the result again gives the result, so the host that was
		// judged is the host that is dialled.
		if port != 80 && port != 443 {
			t.Fatalf("accepted port %d", port)
		}
		again, err := CheckHostPort(got, port)
		if err != nil || again != got {
			t.Fatalf("%q -> %q -> %q, %v", host, got, again, err)
		}
		if strings.ToLower(got) != got || strings.ContainsAny(got, " \t\r\n\x00/\\@%") || len(got) > 253 {
			t.Fatalf("accepted a non-canonical host %q from %q", got, host)
		}
		if a, ok := parseAddr(got); ok && Forbidden(a) {
			t.Fatalf("accepted forbidden address %q", got)
		}
	})
}

func TestCheckAllowEntry(t *testing.T) {
	ok := map[string]string{
		"example.com": "example.com", " Example.COM ": "example.com", "*.example.com": "*.example.com",
		"*.EXAMPLE.com.": "*.example.com", "registry.npmjs.org": "registry.npmjs.org", "*.co.uk": "*.co.uk",
	}
	for in, want := range ok {
		if got, err := CheckAllowEntry(in); err != nil || got != want {
			t.Errorf("CheckAllowEntry(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "*", "*.", "*.com", "com", "localhost", "*.localhost", "db.internal", "10.0.0.1", "1.2.3.4", "::1",
		"example.com:443", "https://example.com", "example.com/x", "user@example.com", "ex*mple.com", "**.example.com",
		"example.*", "a b.com", "a\nb.com", strings.Repeat("a", 260) + ".com", "0x7f.1", "*.127.0.0.1",
	} {
		if got, err := CheckAllowEntry(in); err == nil {
			t.Errorf("CheckAllowEntry(%q) = %q, want a refusal", in, got)
		}
	}
}

func TestMatchHost(t *testing.T) {
	cases := []struct {
		entry, host string
		want        bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "www.example.com", false},
		{"example.com", "notexample.com", false},
		{"*.example.com", "www.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "evilexample.com", false},
		{"*.example.com", "example.com.evil.test", false},
		{"*.example.com", ".example.com", false},
		{"", "example.com", false},
		{"example.com", "", false},
		{"*.", "x.", false},
	}
	for _, c := range cases {
		if got := MatchHost(c.entry, c.host); got != c.want {
			t.Errorf("MatchHost(%q, %q) = %v, want %v", c.entry, c.host, got, c.want)
		}
	}
}

func TestCheckAllowListDropsAndBounds(t *testing.T) {
	in := []string{"b.test", "Bad Entry", "B.test", "*.a.test", "", "*.com"}
	kept, dropped := CheckAllowList(in)
	if strings.Join(kept, ",") != "b.test,*.a.test" || len(dropped) != 3 {
		t.Fatalf("kept %v dropped %v", kept, dropped)
	}
	var many []string
	for i := 0; i < MaxEgressEntries+5; i++ {
		many = append(many, strings.Repeat("a", 1+i%20)+"."+strings.Repeat("b", 1+i/20)+".test")
	}
	kept, dropped = CheckAllowList(many)
	if len(kept) != MaxEgressEntries || len(dropped) != 5 {
		t.Fatalf("kept %d dropped %d", len(kept), len(dropped))
	}
}
