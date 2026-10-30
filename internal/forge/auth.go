package forge

import (
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/vaultenv"
)

// RemoteAuth returns the environment and git arguments that let git and the
// GitHub CLI act with the GITHUB_TOKEN (or GH_TOKEN) the secrets vault granted
// this machine: the token for the child process only, and a credential helper
// for that one host that reads it through gh, so the token is never a git
// argument or in .git/config. On Gitea or Forgejo it is the host's forge token
// (ForgeToken, the vault's first), read by a helper from the child's
// environment; on GitLab the same, but only a token named for the host
// (BELAI_FORGE_TOKEN_<HOST>), so git that worked with the machine's own
// credential keeps doing so. It returns nothing for any other remote, or when
// no such token is held, and then git behaves as it always did.
//
// A fleet push and the git sync's fetch both use it.
func RemoteAuth(rem Remote, now time.Time) (env, gitArgs []string) {
	if rem.Host == "" {
		return nil, nil
	}
	if rem.Kind == KindGitea || rem.Kind == KindForgejo || rem.Kind == KindGitLab {
		token, from := ForgeToken(rem.Kind, rem.Host)
		// On GitLab only a token named for the host is used: a GITLAB_TOKEN
		// set for glab must not replace the git credential pushes already use.
		if token == "" || (rem.Kind == KindGitLab && from != HostTokenEnv(rem.Host)) {
			return nil, nil
		}
		return []string{from + "=" + token},
			[]string{"-c", "credential.https://" + rem.Host + ".helper=", "-c", "credential.https://" + rem.Host +
				`.helper=!f() { test "$1" = get || exit 0; echo username=oauth2; echo "password=$` + from + `"; }; f`}
	}
	if rem.Kind != KindGitHub {
		return nil, nil
	}
	token := ""
	for _, kv := range vaultenv.Default.Environ(now) {
		if name, val, ok := strings.Cut(kv, "="); ok && val != "" && (name == "GH_TOKEN" || (name == "GITHUB_TOKEN" && token == "")) {
			token = val
		}
	}
	if token == "" {
		return nil, nil
	}

	return []string{"GH_TOKEN=" + token, "GITHUB_TOKEN=" + token},
		[]string{"-c", "credential.https://" + rem.Host + ".helper=", "-c", "credential.https://" + rem.Host + ".helper=!gh auth git-credential"}
}
