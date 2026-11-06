# Commit workflow (`/commit` and `commit_mode`)

**Status:** new.

Belai can commit the files a session changed. The harness decides which paths
belong to the session, which are left alone, when to commit and what the
trailers are. A model, only when you turn on `commit_draft`, supplies the words
of the message. No model is ever asked whether a path or a commit is safe, and
nothing is pushed.

The workflow is off by default and runs in the interactive session only.
Headless runs, the editor protocol and remote-control sessions do not use it.

## Modes

`commit_mode` has three values.

| Value | After a completed goal | `/commit` |
| --- | --- | --- |
| `off` | Nothing. The workflow reads nothing and tracks nothing. | Refuses and says how to turn it on. |
| `ask` | Shows the files and the message and commits when you press `y`. | Shows the same question first. |
| `auto` | Commits the files the goal changed from a clean state. | Commits without a question. |

The older `auto_commit_per_task: true` still means `auto` while `commit_mode` is
unset. A value you set in `commit_mode` always wins, and `/autocommit on|off`
sets `auto` or clears it for the session.

## What gets committed

At the start of every prompt the harness reads `git status` (renames by both
names, untracked files one by one) and `HEAD`. The tools' own file-change
reports say which paths the prompt changed. After the prompt each dirty path
lands in exactly one list.

| List | Meaning | Committed |
| --- | --- | --- |
| Task | Changed by the session, and clean when the session first touched it. | Yes |
| Mixed | Dirty before the session touched it, and touched by it. Your earlier edits cannot be told from the session's. | Never |
| Withheld | A rule refused it: a credential store (`.env`, `*.pem`, `id_rsa` and the like), Git's own files, Belai's state and scanner evidence under `.vulnetix`, a `commit_exclude` match, or a file over 8 MiB. | Never |
| Already changed | Dirty before the prompt and not touched by it. | Never |
| Outside the tools | Dirty now, clean before, and not reported by a tool (an editor's save, a build). | Never |

Only Task paths reach `git commit --only`, which keeps anything you had already
staged out of the commit. The harness never runs `git add -A` without a path
list, never passes `--no-verify` (your hooks run) and never pushes.

A file the session edited in one prompt stays the session's in later prompts, so
refining it is not mistaken for one of your edits. A file you edit by hand
between prompts, after the session first changed it, is committed whole; the
question in `ask` mode lists every path so you can see it.

## When it refuses

Every refusal leaves the working tree and the index as they were, and says why
in one line.

- The tree could not be read before the prompt, or now (a git error, a timeout,
  more than 20000 dirty paths).
- `HEAD` moved since the prompt began (a commit, or a pull from git sync).
- A rebase, merge, cherry-pick, revert or bisect is in progress.
- In `auto` mode, any path is Mixed. `ask` and `/commit` commit the Task paths
  and list the Mixed ones as left alone.
- A hook refuses the commit. The paths staged for it are put back.
- In `ask` mode, the tree no longer holds exactly the shown paths when you
  press `y`, or a new prompt started while the question was open. An unanswered
  question is a denial: closing it, `n` and `esc` commit nothing.

## `/commit`

`/commit [message]` commits the paths the session changed from a clean state
since the session began or since they were last committed. With a message it
must be a conventional commit (a lowercase type from `feat`, `fix`, `docs`,
`style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore` or `revert`, an
optional scope and `!`, a lowercase description with no trailing period, a header
of at most 72 characters, a blank line and body lines of at most 72). Trailer
lines such as `Co-authored-by` and `Signed-off-by` are removed from it. Without
one the message is the goal's objective or, with no goal, a line counting the
files.

With nothing to commit it prints what it found and commits nothing. It refuses
while a turn runs, while a commit is in progress and when `commit_mode` is `off`.
For `/commit`, every other dirty path is reported as outside the tools, because
the command does not keep a per-prompt snapshot of what was dirty.

## Drafted messages

With `commit_draft` on, the main model writes the message. It is shown the
cleaned file paths, a line of `+added -removed` counts per file and, after a
goal, the goal's objective. It never sees a diff or a file. Both the facts going
out and the draft coming back are admitted by the security classifier (the
`commit_draft` kind in `tools.classifierKinds`). The draft passes through the
text sanitiser, loses any code fence and any trailer line, and must pass the
conventional-commit check above. A refusal, an error or a bad draft falls back to
the deterministic message, with a one-line note saying the draft was not used.
The drafting turn is the `commit_draft` use case, which routing can send to
another model (`routing.use_cases.commit_draft`).

When Belai runs inside a Pix sandbox the harness appends its attribution
trailers (see [Bot attribution](bot-attribution.md)). Elsewhere it adds none.

## Settings

| Key | Meaning | Default |
| --- | --- | --- |
| `commit_mode` | `off`, `ask` or `auto`. Your own layers only. | `off` |
| `commit_draft` | A model writes the words of the message. Your own layers only. | `false` |
| `commit_exclude` | Relative path globs the harness never commits, at most 64 of at most 200 characters, no `..`. `*.log` matches at any depth, `build/` names a directory, `generated/api` names a path. | none |

A project file (`.vulnetix/settings.json`) cannot set `commit_mode` or
`commit_draft`; the value is dropped with a note. It may add `commit_exclude`
patterns, never remove one of yours, and an invalid pattern is dropped with a
note rather than stopping startup. Neither `commit_mode` nor `commit_draft` is a
web project preference. The settings screen has the rows "commit mode" and
"model-drafted commit message"; both always save to your global settings.

## Records and audit

Each commit or refusal appends one entry to
`.vulnetix/belai/commits/<session>.json` (mode 0600, directory 0700, atomic
write, no symlink followed). It holds the mode, the outcome (`committed` or
`refused`), the full commit id, the branch, a harness-worded reason, the number
of paths committed and the left-behind paths, cleaned and capped at 100 per list.
It never holds the message. The audit log gets a `repo.commit` event whose
outcome is `commit_ask` (you answered or asked), `auto_commit` (no one was
asked) or `refused`, with the commit id and no message.

## Where it lives

| Piece | Code |
| --- | --- |
| Dirty read, split, glob matching | `internal/forge/dirtysplit.go` (`DirtyPaths`, `SplitDirty`, `DirtySplit`, `MatchExclude`) |
| Session ledger of owned and mixed paths | `internal/forge/ledger.go` |
| Message checks, `StripTrailers`, `CleanDraft`, `DiffStat` | `internal/forge/commitmsg.go` |
| Records | `internal/forge/commitrecord.go` |
| The commit itself | `internal/forge/commit.go` (`CommitPaths`) |
| Flow, question and `/commit` | `internal/tui/commit_flow.go` |
| Draft facts and payload | `internal/rolemanager/commitdraft.go` |
| Settings | `internal/config/commit.go` |
