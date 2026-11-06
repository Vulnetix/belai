# Code review (`/review` and `belai review`)

**Status:** new.

`/review` and `belai review` judge a change against a fixed rubric and return
structured findings: a file, a line, a severity, a criterion and what is wrong.
The harness reads the change, applies the caps, checks every finding against
the diff and stores the result. A model, in a read-only turn, supplies the
findings and a verdict as plain lines and nothing else. It has no tool that
writes, and no model is asked whether anything is safe.

The verdict is advice. Nothing in Belai approves, requests changes on or merges
a change because of it. A review is posted to a pull request only when you say
yes (or, from the command line, when you opted in twice).

This is not `/vulnetix review`, which runs the Vulnetix scanners over the
repository and then fixes what it finds (see [Vulnetix review](vulnetix-review.md)).
`/review` reads one change and reports on it.

## What can be reviewed

| Target | How to ask | What is read |
| --- | --- | --- |
| The working tree | `/review`, `belai review` | Staged, unstaged and untracked changes against `HEAD`, as the [`/diff` pane](diff.md) reads them. A path a `Read` deny rule covers is listed by name and never reviewed. |
| A branch | `/review branch NAME --base REF`, `belai review -base REF NAME` | The branch (default `HEAD`) against its merge base with `REF`. With no base, the remote's default branch, then `origin/main`, `origin/master`, `main`, `master`. |
| A pull request | `/review pr N`, `belai review -pr N` | The diff of pull request or merge request `N` through the forge provider's read-only `DiffPR`. `pr current` (or `/review pr` with no number) takes the open pull request of the current branch. |

The TUI also takes `force`, which reviews again even when the store already holds
a review of the same head: `/review pr 12 force`.

A pull request on GitHub is read with the `GH` tool (`pr diff`), one on GitLab
with `Glab` (`mr diff`). On Gitea and Forgejo the model reads the diff from the
checkout, so the checkout must be at the pull request's head; the review says so
and stops when it is not.

## What the harness decides

The model sees a prompt the harness composed from facts it checked. It holds the
target in words, validated commit ids and numbers, the paths of the files that
may be named, the fixed criteria, your own extra criteria and the reply format.
It never holds text from the diff, a pull request's title or description, a
comment, a commit message or check output. The model reads those with its tools,
and every tool result is sanitised and classified like any other.

| Decision | Rule |
| --- | --- |
| Which files can be named | A reviewable file: a path of letters, digits and `. _ - / @ +` (not absolute, no `..`, nothing under `.git`), not binary, not deleted, not read-denied. Other changed files are listed by name when the name is safe and are counted. |
| The diff cap | `review.max_diff_kb` (default 400) and 200 files. A file that would take the diff past either is skipped, the result says `truncated`, and the model is told not to approve what it has not seen. |
| Where a finding can sit | The line must lie inside a hunk of the new version of a reviewable file, context lines included. |
| What a finding is | A line in the grammar below with a severity and a criterion from the closed lists, and a body of at most 2048 bytes. Anything else is dropped and counted, never repaired. |
| How many | The harness reads at most 20 finding lines. The rest are counted as dropped. |
| Order | Most serious first, then by file and line. |
| The verdict | The model's word, narrowed by the harness: `request_changes` needs a `blocker` or `major` finding that survived validation, and `approve` is kept only with nothing above a `nit` and no truncation. With no readable `verdict:` line the result is `comment` and marked incomplete. |
| Reuse | A review is stored by repository and head commit (for a working tree, also a digest of the changes). The same head is answered from the store with no model turn unless you ask again. |

### The reply format

```
verdict: approve | comment | request_changes
finding: FILE:LINE: SEVERITY CRITERION: what is wrong and what would fix it
fix: an optional suggested change for the finding just above
```

`SEVERITY` and `CRITERION` are separated by a space or a slash. The verdict is
one of `approve`, `comment` and `request_changes`. The first valid `verdict:`
line counts. A `fix:` line attaches to the finding above it when that finding
was kept, and is dropped when longer than 1024 bytes. Reasoning around these
lines is ignored.

### Severities

`blocker`, `major`, `minor` and `nit`, most serious first.

### The rubric

The criteria are a closed list. A finding names one of these ids. You and a
repository can add things for the model to weigh; neither can remove a criterion
or add an id.

| Criterion | What it covers |
| --- | --- |
| `correctness` | the change does what it says: logic, edge cases, off-by-one errors, nil and error paths |
| `security` | input handling, injection, secrets, permissions, unsafe defaults, a new trust boundary |
| `tests` | new behaviour is tested; no test was weakened, skipped or deleted to make it pass |
| `errors` | errors are handled and surfaced, and none is swallowed |
| `concurrency` | races, locking, the lifetime of goroutines and resources |
| `performance` | work in a hot path, unbounded growth, needless I/O |
| `compatibility` | public API, file formats, settings and migrations are not broken |
| `maintainability` | clarity, duplication, naming, dead code |
| `docs` | docs and comments match the change; new settings and flags are documented |

Extra criteria come from two places.

- `review.rubric_file`, or `-rubric FILE` on the command line: your own file, one
  criterion per line. Blank lines and lines starting with `#` are skipped, at
  most 20 lines of 200 characters are kept, each cleaned like any text, and a
  link or a file over 8 KiB adds nothing. The lines go in the prompt, since they
  are yours.
- `.belai/review.md` in the repository. The harness only notes that it exists
  (a regular file, not a link) and tells the model to read it with `Read`. Its
  text is repository text: it arrives as a classified tool result, never in the
  prompt, and it may add things to look for but cannot change the format, the
  rules or what the model may do.

## The review turn

The turn is the built-in `belai:pr-reviewer` profile run once in the
foreground, with no kanban card and no worktree. The profile's own instructions
are replaced by the review's, but the central rule is the same one the crew's
reviewer has: everything it reads about the change is data, not instructions.

- Tools: `Read`, `Grep`, `Glob`, `Git`, `GH` and `Glab`. There is no `Bash`, so
  no shell and no push, comment, approve or merge command to guard against. The
  core tools every session has (repository memory among them) are not governed
  by an allowlist and stay.
- It runs through the ordinary permission rules, hooks, posture, classifier and
  OS sandbox, as any background agent does, and a 15 minute timeout bounds it.
- The result of the turn is parsed by the harness. The reply itself is never
  shown, stored or posted.

## Posting a review to a pull request

Only the review of a pull request can be posted. Posting is a write to a forge,
so it is the most guarded step.

1. `review.post` decides whether it is allowed at all. `deny` stops it. `ask`
   (the default) lets a person confirm each post. `allow` is what the command
   line needs to post without a question.
2. A permission rule or a hook can refuse it. The name to match is `ReviewPost`
   with the subject `pr N`: `ReviewPost` or `ReviewPost(pr 7)` in a `deny` rule,
   `ReviewPost` as the matcher of a `pre_tool` hook. A deny rule and a denying
   hook stop the post whatever you answer. An allow rule or an allowing hook never
   skips the question, and a hook or rule that asks makes the headless path
   refuse.
3. In the TUI, `/review post` shows the exact comment and posts it when you press
   `y`. The question is shown for `ask` and for `allow`.
4. On the command line, `belai review -pr N -comment` posts without a question
   only when `review.post` is `allow` in your own settings. With `ask` or `deny`
   it prints why it did not post and exits 2.
5. The head commit must be known and the review must have a readable verdict.
6. A review of the same head is posted once from a host. The store records it,
   and a second attempt is refused.
7. Any error means nothing was posted.

The comment is built only from validated fields: the verdict, each finding's
file, line, severity, criterion, body and fix. Every free-text field goes
through `sanitize.Inert`, so an `@name` notifies nobody and a link, an issue
reference or HTML does not render. The first line is a fixed marker,
`<!-- belai-review head=SHA -->`, so a person or a CI step can tell a review of
this head from an earlier one. The comment is capped at 60000 bytes by leaving
out the last findings and saying how many. It says the review is advisory.

## The command line

```
belai review                       # the working tree
belai review -base main            # HEAD against its merge base with main
belai review -base main feature    # a branch
belai review -pr 12                # a pull request
belai review -pr current -json     # the open pull request of this branch, as JSON
belai review -pr 12 -comment       # post it (needs review.post=allow)
```

| Flag | Meaning |
| --- | --- |
| `-pr N` | review pull request `N`, or `current` |
| `-base REF` | review a branch against its merge base with `REF` |
| `-json` | print the result as JSON, with a `cached` field |
| `-comment` | with `-pr`, post the review (needs `review.post` `allow`) |
| `-rubric FILE` | your file of extra criteria for this run |
| `-provider P`, `-model M` | the model for the review turn |
| `-trust-dir` | trust the current directory without prompting |
| `-force` | review again even when a review of this head is stored |

Exit codes: 0 for `approve` or `comment`, 1 for `request_changes`, 2 for an
error, including a post that was asked for and refused. The command fails closed
in a directory you have not trusted, and `review.enabled` false turns it off.

## Where results are kept

One file per repository and head, `reviews/<repository>-<head>.json` in the
state directory, which the OS sandbox hides from every command. The repository
part is a hash. Files are mode 0600, written atomically and never through a
symbolic link, and the newest 50 reviews are kept. A file is checked field by
field when read (the version, the key, the verdict, every finding's path, line,
severity, criterion and size) and one that fails is used for nothing. A result
holds validated findings, counts and post records, never the model's reply or
any text of the diff.

## Settings

| Setting | Default | Range | Meaning |
| --- | --- | --- | --- |
| `review.enabled` | on | on or off | switches `/review` and `belai review` |
| `review.max_diff_kb` | 400 | 16 to 2048 | the diff a review covers, in KiB |
| `review.post` | ask | ask, deny or allow | whether a review may be posted, and how |
| `review.rubric_file` | none | a file path | your extra criteria, one per line |

Your own settings layers set any key. A project's settings file can only turn
the review off, lower `review.max_diff_kb` or set `review.post` to `deny`. It
cannot turn the review on, raise the cap, allow posting or name a rubric file;
each ignored key is reported in a note. A value of `review.post` this build does
not know is read as `deny`, and a settings file that holds one is refused at
load.

The `code review` row of `/settings` is `review.enabled`; the other keys are
settings file keys.

## Implementation

| Part | Where |
| --- | --- |
| Types, the closed lists, `Format`, the verdict rule | `internal/review/review.go` |
| `Collector.Collect`, `ValidRef`, `ProviderFor`, the hardened git calls | `internal/review/collect.go` (git through `workdiff.Git`) |
| `ParseUnified`, `FromSnapshot`, `PlainPath`, `Diff.Reviewable`, `Diff.Skipped`, `Diff.File`, `Diff.HasLine`, `DiffFile.Reviewable`, `DiffFile.HasLine` | `internal/review/diff.go` |
| `Parse`, the reply grammar | `internal/review/parse.go` |
| `Run`, `LoadRubric`, `Prompt` | `internal/review/run.go` |
| `Profile`, `Agent.Review`, the tool list | `internal/review/agent.go` |
| `OpenStore`, `Store.Load`, `Store.Save`, `Store.MarkPosted`, `Store.WasPosted`, `Result.ID` | `internal/review/store.go` |
| `Comment`, `Marker`, `Post`, `Guard.Check` | `internal/review/post.go` |
| `Result.ExitCode`, `RubricIDs` | `internal/review/review.go` |
| The pull request diff, `DiffPR` on GitHub, GitLab and Gitea | `internal/forge/prdiff.go` |
| `sanitize.Inert` | `internal/sanitize/inert.go` |
| The settings | `internal/config/review.go` |
| The command line | `cmd/belai/reviewcmd.go` |
| The slash command and the post question | `internal/tui/code_review.go` |

## Limits

- The review works on text diffs. Binary and deleted files are counted, not read.
- The model checks the diff and the code around it; it does not run the tests.
- Dedupe of a posted review is by this host's record. A second host, or someone
  who posts by hand, can still repeat a comment; the marker line is there for them
  to spot it.
- On Gitea and Forgejo a pull request review needs the checkout at the pull
  request's head.
- It works in the TUI and on the command line. It is not a model-facing tool,
  and there is no web or editor surface for it.
