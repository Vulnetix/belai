# Forges

Belai works with repositories on github.com, GitHub Enterprise Server (GHES),
Gitea, Forgejo (Codeberg included) and GitLab. The forge decides which tool
reads pull requests and CI, how a worker's branch is published, and what a
Software Factory clone request may name.

The code is `internal/forge`: `remote.go` reads the `origin` remote,
`hosts.go` holds the `forges` setting, `provider.go` drives `gh` and `glab`,
and `gitea.go` drives the Gitea API, which Forgejo serves too.

## Which forge a remote is on

`forge.ParseRemote` reads the host from the `origin` URL (https, ssh or scp
form). The kind is, in order:

1. the `forges` setting, when it names the host;
2. `codeberg.org`, which is Forgejo;
3. the host name: `*.github.com` and `github.*` are GitHub, a host containing
   `gitlab` is GitLab;
4. otherwise a plain git remote, with no pull request or CI support.

A Gitea or Forgejo host is never guessed from its name. A remote on
`gitea.example.com` is a plain git remote until the `forges` setting names it,
because Belai would otherwise send a token to whatever host a clone's remote
points at.

## The forges setting

`forges` maps a host to its kind: `github`, `ghes`, `gitea`, `forgejo` or
`gitlab`. `ghes` is driven by `gh` like github.com.

```json
{
  "forges": {
    "git.example.com": "gitea",
    "code.example.org": "forgejo",
    "ghe.example.net": "ghes",
    "gitlab.example.com": "gitlab"
  }
}
```

The setting is read from your own settings only, never a project's. A
project's `.vulnetix/belai/settings.json` cannot add or change a host: the
entry is dropped with a note when the settings are resolved. A repository can never
make Belai send a forge token to a host the user did not name. The host is
lower-cased and names no port; an unknown kind, an empty host or a host with a
scheme, port or path is refused when the settings are validated.

## Tokens

GitHub and GHES use the GitHub CLI's sign-in (`gh auth login`, with
`--hostname` for GHES) and GitLab uses `glab`'s, as before. A fleet push to
GitLab uses the machine's own git credential unless
`BELAI_FORGE_TOKEN_<HOST>` is set for that host; `GITLAB_TOKEN` alone never
replaces it.

Gitea and Forgejo have no CLI Belai drives. It talks to `https://<host>/api/v1`
with a personal access token read from the first variable of these that is
set:

| Kind | Variables, in order |
|------|---------------------|
| `gitea` | `BELAI_FORGE_TOKEN_<HOST>`, `GITEA_TOKEN` |
| `forgejo` | `BELAI_FORGE_TOKEN_<HOST>`, `FORGEJO_TOKEN`, `GITEA_TOKEN` |

`<HOST>` is the host upper-cased with every character but a letter or a digit
turned into `_`: `codeberg.org` is `BELAI_FORGE_TOKEN_CODEBERG_ORG`. A token
the [secrets vault](vault-env.md) granted this machine is read before the
process environment. The token needs the `read:repository` and
`write:repository` scopes, and `write:issue` to comment on pull requests.

The token never appears in a URL or a command line. git reads it through a
credential helper for that one host, which takes it from the git process's own
environment: `credential.https://<host>.helper` answers `username=oauth2` and
the token. The API calls send it as `Authorization: token <token>`.

## What works where

| | GitHub, GHES | GitLab | Gitea, Forgejo |
|-|--------------|--------|----------------|
| Driven by | `gh` | `glab` | the REST API |
| Pull request for a branch, open, list, close, comment | yes | yes | yes |
| Draft pull request | native | native | the `WIP: ` title prefix |
| CI for the CI tab and gates | check runs | pipeline jobs | the head commit's statuses (Gitea and Forgejo Actions, Woodpecker and Drone report there) |
| Fleet publishing (`PublishBranch`) | yes | yes | yes |
| Merge-conflict cards | yes | yes | yes |
| The forge coordinator | yes | not available | not available |
| Software Factory clones (`belai rc`) | yes | yes | yes |

The forge coordinator, which takes over a push that failed for the machine's
credential, holds a GitHub App token. It is not available on any other forge,
and a failed push there reads as the failure it is
([fleet.md](fleet.md#the-forge-coordinator)).

A worker's Bash may not open, merge, approve or comment on a pull request with
`tea` or `fj` either, as with `gh` and `glab` ([fleet.md](fleet.md#publishing)).
When `tea` is installed and signed in, the read-only `Tea` tool lists pull
requests, issues, releases, repositories and branches; its results are
classified like `GH`'s and `Glab`'s.

## Repository keys

The kanban board's `remote`, a merge-conflict card's `repo` and a Software
Factory clone name a repository by `owner/name` on github.com and by
`host/owner/name` on any other forge, lower-cased. A GitLab owner may be a
group path (`gitlab.example.com/group/sub/app`). So `acme/app` on github.com
and `acme/app` on a Gitea stay apart, and a card for one is never claimed by a
checkout of the other. An item or card written before remotes off github.com
kept their host carries the bare `owner/name`; it still matches the checkout
it was filed from (`kanban.SameRemote`), so no older card is stranded.
