package kanban

import "testing"

func TestRemotesKeepTheirHostAndStillMatchTheOldForm(t *testing.T) {
	cases := []struct {
		a, b string
		same bool
	}{
		{"acme/api", "acme/api", true},
		{"gitlab.example.com/group/app", "group/app", true},
		{"codeberg.org/acme/app", "acme/app", true},
		{"codeberg.org/acme/app", "git.example.com/acme/app", false},
		{"acme/api", "acme/other", false},
		{"group/sub/app", "sub/app", false},
		{"", "acme/api", false},
	}
	for _, c := range cases {
		if got := SameRemote(c.a, c.b); got != c.same {
			t.Errorf("SameRemote(%q, %q) = %v", c.a, c.b, got)
		}
	}
	if LegacyRemote("acme/api") != "" || LegacyRemote("git.example.com:3000/acme/app") != "acme/app" {
		t.Error("legacy form")
	}
	r := ClaimRequest{Project: "app", Remote: "gitlab.example.com/group/app"}
	if !r.sameRepository(Item{Project: "x", Remote: "group/app"}) || !r.sameRepository(Item{Project: "group/app"}) {
		t.Error("an item filed with the old owner/name remote is no longer claimable")
	}
	if r.sameRepository(Item{Project: "x", Remote: "other/app"}) {
		t.Error("another repository matched")
	}
}
