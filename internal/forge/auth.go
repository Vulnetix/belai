package forge

import (
	"strconv"
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
	token := VaultGitHubToken(now)
	if token == "" {
		return nil, nil
	}

	return []string{"GH_TOKEN=" + token, "GITHUB_TOKEN=" + token},
		[]string{"-c", "credential.https://" + rem.Host + ".helper=", "-c", "credential.https://" + rem.Host + ".helper=" + GitHubCredentialHelper}
}

// VaultGitHubToken is the GitHub token the secrets vault grants this machine's
// commands at now: GH_TOKEN, else GITHUB_TOKEN, else empty. The caller must
// never put it in a result, a log or an error.
func VaultGitHubToken(now time.Time) string {
	token := ""
	for _, kv := range vaultenv.Default.Environ(now) {
		if name, val, ok := strings.Cut(kv, "="); ok && val != "" && (name == "GH_TOKEN" || (name == "GITHUB_TOKEN" && token == "")) {
			token = val
		}
	}
	return token
}

// GitHubCredentialHelper is the git credential helper for github.com: gh
// answers with the GH_TOKEN or GITHUB_TOKEN in the child's environment.
const GitHubCredentialHelper = "!gh auth git-credential"

// githubHelperKey is the git config key RemoteAuthEnv sets.
const githubHelperKey = "credential.https://github.com.helper"

// RemoteAuthEnv is RemoteAuth's credential helper for a command the model runs
// (Bash, a native tool), where the harness does not build the git argv. When
// the vault holds a GitHub token at now, it returns env with GIT_CONFIG_KEY_n
// and GIT_CONFIG_VALUE_n entries that reset the helper for https://github.com
// and then set it to gh, so a clone or push of a private repository
// authenticates without the token being a git argument or in a config file.
// The two entries go after any GIT_CONFIG_* env already holds, and
// GIT_CONFIG_COUNT is replaced by the new total. The token itself is not
// added: the vault's GH_TOKEN or GITHUB_TOKEN is already in the child's
// environment. Without a token env is returned unchanged.
func RemoteAuthEnv(env []string, now time.Time) []string {
	if VaultGitHubToken(now) == "" {
		return env
	}
	next := 0
	out := make([]string, 0, len(env)+5)
	for _, e := range env {
		key, val, _ := strings.Cut(e, "=")
		if key != "GIT_CONFIG_COUNT" {
			out = append(out, e)
			continue
		}
		// The last one wins, as it does in the child.
		next = 0
		if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && n > 0 {
			next = n
		}
	}
	reset, set := strconv.Itoa(next), strconv.Itoa(next+1)
	return append(out,
		"GIT_CONFIG_KEY_"+reset+"="+githubHelperKey, "GIT_CONFIG_VALUE_"+reset+"=",
		"GIT_CONFIG_KEY_"+set+"="+githubHelperKey, "GIT_CONFIG_VALUE_"+set+"="+GitHubCredentialHelper,
		"GIT_CONFIG_COUNT="+strconv.Itoa(next+2),
	)
}
