package tools

import "testing"

// TestGHAPIGate verifies the second gate for the GH native tool. Allowed
// commands are read-only GETs against the approved path families; everything
// else is refused.
func TestGHAPIGate(t *testing.T) {
	allowed := []string{
		"api repos/Owner/Repo/contents/.github/workflows/pages.yml",
		"api repos/Owner/Repo/git/trees/HEAD?recursive=1",
		"api orgs/Owner/repos --paginate",
		"api --method GET repos/Owner/Repo",
		"api /repos/Owner/Repo",
		"api graphql -f query=query{viewer{login}}",
		"api users/octocat",
		"api user",
		"api user/repos",
		"api search/code?q=foo",
		"api rate_limit",
		"api meta",
		"api gitignore/templates/Go",
		"api licenses/mit",
	}
	for _, c := range allowed {
		if err := ghAPIGate(c); err != nil {
			t.Errorf("ghAPIGate(%q) = %v, want nil", c, err)
		}
	}

	blocked := []string{
		"api -X POST repos/Owner/Repo/issues",
		"api -X post repos/Owner/Repo/issues",
		"api -f name=x repos/Owner/Repo",
		"api --input body.json repos/Owner/Repo",
		"api admin/hooks",
		"api repos/Owner/Repo extra/path",
		"api --bogus repos/Owner/Repo",
		"api graphql -f query=mutation{addStar}",
		"api graphql -f query='mutation{addStar}'",
		"api",
		"api --method POST repos/Owner/Repo",
		"api --method=POST repos/Owner/Repo",
		"api repos/Owner/Repo --method PUT",
	}
	for _, c := range blocked {
		if err := ghAPIGate(c); err == nil {
			t.Errorf("ghAPIGate(%q) = nil, want error", c)
		}
	}
}

// TestGHAPIGateCaseFoldsMethod verifies the HTTP method check is not case
// sensitive.
func TestGHAPIGateCaseFoldsMethod(t *testing.T) {
	for _, m := range []string{"GET", "get", "Get", "HEAD", "head"} {
		cmd := "api -X " + m + " repos/Owner/Repo"
		if err := ghAPIGate(cmd); err != nil {
			t.Errorf("ghAPIGate(%q) = %v, want nil", cmd, err)
		}
	}
}
