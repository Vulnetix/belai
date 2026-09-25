// Package forge gathers the git and code-forge facts behind the runs panel's
// git and ci tabs: the current branch, the origin remote (credentials masked),
// the worktree list, and — through the provider's own CLI (gh for GitHub,
// glab for GitLab) — the pull or merge request for the branch and its CI
// checks. It also carries the few user-initiated mutations those tabs offer:
// adding and removing a worktree, pushing a branch, and opening a PR/MR.
//
// Unlike internal/gitinfo, which never shells out because it runs on every
// footer refresh, this package execs git and the provider CLIs. Every call is
// argv-only (never a shell), bounded by a timeout, and run in its own process
// group. The CLIs manage their own credentials, so the environment is passed
// through unchanged.
//
// Everything a provider CLI prints is third-party text. It is cleaned (harness
// delimiter markup, control and bidi runes removed, length capped) before it
// is stored, and it is only ever rendered in the TUI: none of it enters a
// model turn.
package forge
