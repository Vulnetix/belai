# Git sync

Before a session starts work, Belai brings its branch up to date with upstream main, so the work does not start from a base that has moved on and collide with it later. It is on by default and every session can turn it off.

## What it does

It runs before a turn, never during one:

- **The first turn of a session**, and
- **the first turn after a commit** (HEAD moved since the last sync).

For each run, in the session's directory:

1. It checks the repository is quiet (see below). If not, it skips and says why.
2. `git fetch --prune origin`.
3. It finds origin's default branch (`origin/HEAD`, else `origin/main`, else `origin/master`).
4. If the branch already contains it, nothing moves.
5. Otherwise it runs `git rebase --rebase-merges origin/<default>`.

On the default branch this is a plain `git pull --rebase`. On a feature branch, a linked worktree or a checked-out PR it replays the branch's commits on top of the newest upstream main, so the PR target's changes are already in the branch before more work lands on it.

Each run writes one line to the transcript, which the website shows: what moved (`rebased feature/x onto origin/main (3 commits new, abc1234 to def5678)`), that nothing was needed, or why it did not run.

## When it does not run

It acts only on a quiet repository, and otherwise skips and tells you once:

| Skipped when | Reason shown |
| --- | --- |
| tracked files have uncommitted changes | `uncommitted changes` |
| a rebase, merge, cherry-pick, revert or bisect is in progress | `a rebase in progress`, ... |
| HEAD is detached | `detached HEAD` |
| there is no `origin` remote | `no origin remote` |
| git has no `user.name` and `user.email` (a fresh machine, a CI runner), which a rebase needs to write its commits | `git has no user.name and user.email set...` |
| origin's default branch cannot be found | `cannot tell origin's default branch` |

Untracked files do not block it: git itself refuses to overwrite one. A skip does not use up the first turn; the next turn start tries again.

## Safety

- **It never stashes, resets or discards anything.** A dirty tree is skipped, not cleaned.
- **A conflict is aborted at once.** `git rebase --abort` runs, the branch is exactly where it was, and the line says `conflicts with origin/main; rebase aborted, branch unchanged`. The session carries on from where it was.
- **A rewrite is reported.** When the rebase moved commits the branch's upstream already has (a pushed branch, a PR), the line says the remote branch updates only with `git push --force-with-lease`. Belai never pushes. The line also gives the old and new short SHA; to get back, `git reset --hard <old>` (or `git reflog`).
- **No prompts, no hooks.** Git runs with `GIT_TERMINAL_PROMPT=0` and no editor, so a call that wants a person fails at once. Repository hooks and the filesystem monitor are switched off for these calls, because they run outside the sandbox.
- **Trusted directories only.** It runs in a directory you have already trusted, like every session.
- **Fleet workers and ACP sessions never run it.** A worker commits on a branch of its own in a worktree the harness manages, and an editor owns the working copy of an ACP session.

## Turning it off

| Scope | How |
| --- | --- |
| This session | `/gitsync off` in the TUI, or the switch on the session's page on the website |
| All future sessions | `/gitsync global off`, or `"git": { "sync": false }` in your settings |
| One headless run | `belai -no-git-sync -prompt ...` |
| A web-started session | untick **Sync with main** in the New session dialog (`belai rc-session -git-sync off` underneath) |
| One repository | `"git": { "sync": false }` in the repository's `.vulnetix/settings.json` |

A repository may opt itself out, and cannot switch the sync on over your own `git.sync: false`. `/gitsync` alone shows the state and the last result; `/gitsync on` and `/gitsync global on` turn it back on.

A switch made on the website for a running session reaches the host with its next heartbeat and is confirmed on the page when the host reports it applied. A later change at the terminal is not overridden by an older one from the website.

## What the website shows

For any session whose directory is a git repository, with or without the sync, the host reports: the branch (or a detached HEAD), whether it is a linked worktree, the short HEAD and its subject, the remote (`owner/repo`), the upstream and how far ahead or behind it is, how far HEAD is from origin's default branch, whether tracked files are dirty, the pull or merge request for the branch (number, title, state, draft, and its checks) through the provider's own CLI (`gh` or `glab`), and the sync switch with its last result. It is read every 30 seconds and after each turn, travels with the session's registration, and never enters a model turn. A host without `gh` or `glab`, or with a remote that has no provider, shows no PR and says why.

## Files

`internal/gitsync` (the sync and the reading), `internal/agent` (the before-turn hook), `internal/rc` and `cmd/belai/rccmd.go` (web-started sessions), `internal/tui/git_sync.go` (`/gitsync`), `internal/sessionsync` (what is reported, and the switch coming back).
