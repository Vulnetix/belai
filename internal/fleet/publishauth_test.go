package fleet

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/vaultenv"
)

func withVaultToken(t *testing.T, vars ...vaultenv.Var) {
	t.Helper()
	vaultenv.Default.Replace(vars, time.Now().Add(time.Hour))
	t.Cleanup(func() { vaultenv.Default.Replace(nil, time.Time{}) })
}

// A GitHub token the vault granted this machine reaches the push and gh, as the
// child's environment and a credential helper for that one host.
func TestPublishAuthHandsTheVaultTokenToAGitHubPush(t *testing.T) {
	withVaultToken(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: "ghp_exampletoken0001"})
	env, args := publishAuth(forge.Remote{Kind: forge.KindGitHub, Host: "github.com"}, time.Now())
	if !slices.Contains(env, "GH_TOKEN=ghp_exampletoken0001") || !slices.Contains(env, "GITHUB_TOKEN=ghp_exampletoken0001") {
		t.Fatalf("env = %v", env)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "credential.https://github.com.helper=!gh auth git-credential") {
		t.Fatalf("args = %v", args)
	}
	for _, a := range args {
		if strings.Contains(a, "ghp_") {
			t.Fatalf("the token reached a git argument: %q", a)
		}
	}
}

func TestPublishAuthIsOffWithoutAVaultTokenOrOutsideGitHub(t *testing.T) {
	if env, args := publishAuth(forge.Remote{Kind: forge.KindGitHub, Host: "github.com"}, time.Now()); env != nil || args != nil {
		t.Fatalf("no token held, yet env=%v args=%v", env, args)
	}
	withVaultToken(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: "ghp_exampletoken0001"})
	if env, args := publishAuth(forge.Remote{Kind: forge.KindGitLab, Host: "gitlab.com"}, time.Now()); env != nil || args != nil {
		t.Fatalf("a GitLab remote was given a GitHub token: env=%v args=%v", env, args)
	}
}

func TestPublishAuthPrefersGHTokenAndIgnoresAnExpiredOne(t *testing.T) {
	withVaultToken(t,
		vaultenv.Var{Name: "GITHUB_TOKEN", Value: "ghp_githubtoken001"},
		vaultenv.Var{Name: "GH_TOKEN", Value: "ghp_ghtokenpreferred1"},
	)
	env, _ := publishAuth(forge.Remote{Kind: forge.KindGitHub, Host: "github.com"}, time.Now())
	if !slices.Contains(env, "GH_TOKEN=ghp_ghtokenpreferred1") {
		t.Fatalf("env = %v", env)
	}
	withVaultToken(t, vaultenv.Var{Name: "GITHUB_TOKEN", Value: "ghp_expiredtoken01", ExpiresAt: time.Now().Add(-time.Minute)})
	if env, _ := publishAuth(forge.Remote{Kind: forge.KindGitHub, Host: "github.com"}, time.Now()); env != nil {
		t.Fatalf("an expired token was used: %v", env)
	}
}

// On Gitea or Forgejo the host's forge token reaches the push through a helper
// that reads it from the child's environment, never from an argument.
func TestPublishAuthUsesTheForgeTokenOnGitea(t *testing.T) {
	withVaultToken(t, vaultenv.Var{Name: "BELAI_FORGE_TOKEN_CODEBERG_ORG", Value: "fj_exampletoken0001"})
	env, args := publishAuth(forge.Remote{Kind: forge.KindForgejo, Host: "codeberg.org"}, time.Now())
	if !slices.Equal(env, []string{"BELAI_FORGE_TOKEN_CODEBERG_ORG=fj_exampletoken0001"}) {
		t.Fatalf("env = %v", env)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "credential.https://codeberg.org.helper=!f()") || !strings.Contains(joined, "$BELAI_FORGE_TOKEN_CODEBERG_ORG") || strings.Contains(joined, "fj_") {
		t.Fatalf("args = %v", args)
	}
	if env, _ := publishAuth(forge.Remote{Kind: forge.KindGitea, Host: "git.example.com"}, time.Now()); env != nil {
		t.Fatalf("another host's token was used: %v", env)
	}
}

// On GitLab only a token named for the host is used, so a GITLAB_TOKEN set for
// glab never replaces the credential a push already used.
func TestPublishAuthOnGitLabNeedsAHostToken(t *testing.T) {
	withVaultToken(t, vaultenv.Var{Name: "GITLAB_TOKEN", Value: "glpat-exampletoken01"})
	if env, args := publishAuth(forge.Remote{Kind: forge.KindGitLab, Host: "gitlab.com"}, time.Now()); env != nil || args != nil {
		t.Fatalf("GITLAB_TOKEN was used: %v %v", env, args)
	}
	withVaultToken(t, vaultenv.Var{Name: "BELAI_FORGE_TOKEN_GITLAB_COM", Value: "glpat-exampletoken02"})
	env, args := publishAuth(forge.Remote{Kind: forge.KindGitLab, Host: "gitlab.com"}, time.Now())
	if !slices.Equal(env, []string{"BELAI_FORGE_TOKEN_GITLAB_COM=glpat-exampletoken02"}) || strings.Contains(strings.Join(args, " "), "glpat-") {
		t.Fatalf("env = %v args = %v", env, args)
	}
}
