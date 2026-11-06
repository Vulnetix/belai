# Worktree sessions (`/worktree` and `belai worktree`)

**Status:** new.

A worktree session is a card on the kanban board that a worker handles inside its
own git worktree. The harness picks the directory, the branch and the base commit,
runs the setup commands you configured before any work starts, and runs your
teardown commands before it removes the directory. A model supplies a card title
and nothing else: it never names an argument, a path, a ref or a command line.

Every worker that uses `workspace.isolation: worktree` already works this way (see
[Fleet](fleet.md)). This page adds the commands you configure for it, the update
from the branch it was made from, and the `/worktree` and `belai worktree`
commands for the worktrees the harness made.

## Using it

| You type | What happens |
| --- | --- |
| `/worktree new TITLE`, `belai worktree new [-agent NAME] [-label L] TITLE` | Files a card with the `worktree` label. A worker that claims it works in a worktree, whatever its profile says about isolation. `-agent` also routes the card to that worker profile. Without it, route the card with `belai kanban assign K-xxxxxx PROFILE`. |
| `/worktree list`, `belai worktree list [-json]` | Lists the worktrees the harness made for this repository: id, branch, whether the directory is still there, and whether it is kept. |
| `/worktree update ID`, `belai worktree update [-yes] ID` | Brings the worktree's branch up to date with the branch it was made from, by `worktrees.update_mode`. Asks first. |
| `/worktree drop ID`, `belai worktree drop [-yes] ID` | Runs the teardown commands, then removes the worktree. Asks first and shows every command it would run. The branch stays when it holds work. |

An id is the name `list` prints, such as `K-3f9a2c-a1b2c3`. It is the only way to
name a worktree: no command takes a path or a ref.

`update` and `drop` ask before they run anything. In the TUI the question is a y/N
prompt with the commands in it. On the command line the question goes to the
terminal; with no terminal and no `-yes` the command refuses, so a script has to
say it means it. Both refuse in a directory you have not trusted (`-trust-dir`
trusts the current directory first), and both refuse while a running agent holds
the worktree's card.

## Setup and teardown

`worktrees.setup` is a list of argv arrays run in order inside the new worktree,
once it exists and before the worker's first turn. `worktrees.teardown` runs in
order before the worktree is removed.

```json
{
  "worktrees": {
    "setup": [["npm", "ci"], ["./scripts/seed-db.sh", "--quiet"]],
    "teardown": [["./scripts/drop-db.sh"]],
    "network": "allow"
  }
}
```

Each argv runs directly, never through a shell, so quoting, pipes and `$VARIABLES`
are not interpreted. It runs with the scrubbed environment (no provider keys, no
`BELAI_` variables), the worktree as its directory and its own process group,
under the same OS sandbox as a `Bash` call: the worktree and the usual tool
caches are writable, nothing else is, and Belai's state directory is hidden. The network is cut unless you set
`worktrees.network` to `allow`. Where the sandbox cannot enforce the cut and the
sandbox mode is not `off`, the command is refused rather than quietly given a
network. Your permission rules apply to each argv as a `Bash` command: a deny rule
refuses it, and an ask rule stops it because nobody can answer there.

A setup that fails (a non-zero exit, a timeout, a refusal) removes the worktree
again and the item goes back to its list as a failed attempt. The release note is
harness text: the phase, the position, the program's base name and how it ended.
The command's output is arbitrary text and does not go on the board. A short tail
of it is sanitised, classified as process output and given only to the setup-debug
turn that follows, in the trusted repository, which records what it finds with
`KanbanUpdate`. Teardown does not run for a worktree whose setup did not finish.

A teardown that fails keeps the worktree. The harness never force-deletes a
directory because a cleanup did not finish; fix the cause and run `/worktree drop`
again. Teardown does not run in a worktree whose `.git` file changed since the
harness pinned it; that one is removed without running your commands in it.

What setup leaves behind as untracked files (`node_modules` where it is not
ignored, a generated directory) is marked as placed and never committed by the
harness. More than 200000 such files fails the setup, as a cache nobody
could review. A setup that rewrites the worktree's `.git` file fails.

The conflict-resolution loop for pull request cards makes its own fresh worktree
for each attempt and does not run setup or teardown.

## Update from the base

A worktree remembers the branch it was made from: the repository's checked-out
branch when the base was `HEAD`, or the profile's `workspace.base` when that is a
branch name. A commit id or a pull request head has no branch, and such a worktree
cannot be updated.

`worktrees.update_mode` is `off` (the default), `merge` or `rebase`.

- `merge` runs `git merge --no-edit --no-ff` of the base's tip, with a commit
  message the harness writes.
- `rebase` runs `git rebase` onto the base's tip. It rewrites the branch's own
  commits, so use it only for a branch nobody else has.

The update runs only between turns, never under a running one: when a worker
resumes a card whose branch already exists, before the model's first turn (the
workspace note then names the base commit it was brought up to), and when you run
`/worktree update`. It refuses unless the worktree is the one the harness made, on
its own `belai/` branch, with no uncommitted change to a tracked file. The base ref
is the one recorded when the worktree was made, checked as a branch name by git; a
remote-tracking ref is fetched first, and a failed fetch uses what the repository
already holds.

A conflict aborts the merge or rebase and leaves the branch where it was. The
answer names the conflicting files as plain note names, at most 12, and the work
goes on with the branch as it stood. The base the work is measured from moves
only after an update succeeded.

## Settings

All of these are your own settings. A project's `.vulnetix/settings.json` cannot
set any of them: it can only set `worktrees.update_mode` to `off`, and every other
key it names is dropped with a note.

| Setting | Default | Range | What it does |
| --- | --- | --- | --- |
| `worktrees.setup` | none | argv arrays | Commands run in a new worktree. |
| `worktrees.teardown` | none | argv arrays | Commands run before a worktree is removed. |
| `worktrees.timeout_seconds` | 120 | 1 to 600 | The bound on one argv. |
| `worktrees.update_mode` | off | off, merge or rebase | How a branch is brought up to date with its base. An unknown value is `off`. |
| `worktrees.keep_on_exit` | on | on or off | Keeps the directory of a `/worktree new` session after its agent finishes. |
| `worktrees.network` | deny | deny or allow | Whether setup and teardown may use the network. |

A list holds at most 8 commands. A command has at most 32 words, each of at most
512 bytes, none with a control character. The program is a bare program name
looked up on `PATH` (letters, digits, `.`, `_`, `+`, `-`), a relative path such as
`./scripts/setup.sh` that does not climb out of its directory, or an absolute path.
A value outside these limits fails the settings load and names the key.

`keep_on_exit` applies only to cards filed with `/worktree new`. A kept worktree
holds its branch, and git lets one worktree hold a branch at a time, so a later
claim of the same card (a reviewer) needs the worktree dropped first.

## What is recorded

For each worktree the harness makes it writes one small file under
`<state>/agents/worktrees/<project key>/<id>.json`, mode 0600, under a lock: the
id, the card's short id, the branch, the base commit and ref, whether it is kept
and when it was made. It holds no path, no output and no model text. The
directory is rebuilt from the id every time. `list`, `update` and `drop` act only
on a worktree that has such a record, that the repository still lists as a
worktree on the recorded branch, and whose `.git` file points into this
repository's own admin directory. The state directory is hidden from the OS
sandbox and is a protected path, so a model cannot edit these records or the
settings that hold the commands.

## Limits

- Setup and teardown argv come from the user's settings only. A repository cannot
  add one, and no model argument reaches an argv, a path or a ref.
- The model is never asked whether a command is safe. Permission rules, hooks on
  the session, the sandbox and the trust gate apply as they do to any command
  Belai runs.
- A worktree command refuses in an untrusted directory.
- There is no model-facing tool that updates or removes a worktree. A model's own
  git works inside its private copy of the git directory, which an update from
  outside would leave stale, so an update waits for the gap between turns.
