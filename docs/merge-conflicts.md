# Merge conflicts

`belai:conflict-resolver` resolves a pull request's merge conflicts. Belai runs
the loop itself, in Go, so its promises hold whatever the model does: at most
five fresh checkouts, each one told what the earlier ones chose and how they
failed, and no checkout allowed to repeat an earlier one's choices. The model
resolves the hunks. Belai decides whether the resolution is new, checks it, and
pushes it.

## The card

The worker claims backlog cards labelled `merge-conflict`. The Software Factory
files one for a pull request that its mirror marks conflicting, on a push to the
default branch and when a pull request is opened, synchronized or reopened, and
a person can file one from the Pix Sandbox Pull requests tab or Checkpoints. One
card is open per pull request at a time.

The card's title is `Resolve merge conflicts: owner/name#N`, its assignee
`belai:conflict-resolver`, its project the repository's name and its remote
`owner/name` in lower case, so a worker on any machine with that repository
claims it. `agent.pr` holds the pull request's link. The body starts with a
fenced block of lines Belai reads:

```
pr: https://github.com/owner/name/pull/N
repo: owner/name
number: N
head: <head ref>
base: <base ref>
fork: true|false
```

Off github.com `repo` is `host/owner/name` and `pr` is the forge's own link:
`https://<host>/<owner>/<name>/pulls/N` on Gitea and Forgejo,
`https://<host>/<group>/<name>/-/merge_requests/N` on GitLab (the group may
be nested) and `https://<host>/<owner>/<name>/pull/N` on GHES.

Every key is required, once. `repo` must be a lower-case `owner/name`
(`host/owner/name` off github.com), `number`
a positive integer, `head` and `base` plain branch names (letters, digits and
`. _ / -`, no leading `-`, no `..`, no empty or dotted segment, no `.lock`
suffix, and `git check-ref-format` must accept them), and the `pr` link must
name the same repository and number. A card that fails any of these, or that
names a repository other than the worker's `origin`, goes to `blocked` with a
note, before anything is fetched.

## The loop

For each attempt `n` from 1 to `kanban.conflicts.max_checkouts` (5 for the
built-in profile):

1. Fetch the pull request's two sides from `origin`: `refs/heads/<head>` (for a
   fork, `refs/pull/N/head`) and `refs/heads/<base>`, into
   `refs/belai/pull/N/head` and `refs/belai/pull/N/base`, with the GitHub token
   the vault grants the machine. This happens once per claim.
2. Make a fresh worktree at the head commit, on a new branch of the card's own
   (`belai/K-xxxxxx/aN`).
3. On the first attempt for a head commit, run the build checks and the linters
   there, before the merge. That is the baseline: a build that already fails on
   the head, and the findings a linter already reports there, are not the
   merge's doing.
4. `git merge --no-ff --no-commit` the base into it, with `merge.conflictStyle`
   set to `diff3` and `rerere` off, so a resolution recorded on the machine never
   answers for the model.
5. Read the unmerged paths from git's index and each conflicted file as the
   merge left it. A file with markers is read hunk by hunk; a binary file, a
   file changed on one side and deleted on the other, or a symlink is one
   whole-file choice, compared by blob id.
6. Plan the strategy for this attempt (see [Strategies](#strategies)).
7. Run one agent turn in the worktree with an attachment Belai composes: the
   attempt number, what current and incoming mean, the conflicted files and the
   line of each hunk, the ledger of earlier attempts, and the strategy, hunk by
   hunk. The turn's directive tells the model not to commit, merge, rebase,
   reset, stash, switch branches or publish.
8. Classify what the turn left and compute its fingerprint. If an earlier
   attempt has that fingerprint, Belai puts the merge back (the branch at the
   head, the index with its unmerged entries, the files with their markers) and
   runs one more turn in the same worktree, saying which attempt it repeated
   and with a strategy that excludes that fingerprint too. That turn does not use
   up the attempt. A second repeat does: it is recorded as `repeat of attempt M`.
9. Run the [checks](#checks) on the resolution.
10. On a pass, commit the merge and [push it](#pushing). On a failure, write the
    attempt's note, delete the worktree and its branch, and start the next
    attempt from step 2.

After the last checkout fails, the card gets the note `conflicts unresolved
after 5 attempts` and goes to `blocked` with the label
`checkpoint:conflicts-unresolved`, so no worker takes it until a person decides
(see [Human checkpoints](kanban.md#human-checkpoints)). A person who sends it
back to the backlog starts a new round
([Retrying an unresolved card](#retrying-an-unresolved-card)).

If the base merges with no conflicts, there is nothing to vary: Belai runs the
checks and pushes the merge when they pass, or blocks the card with a note
naming the failing check when they do not.

The claim's lease is renewed for the whole loop, every turn counts against the
same per-item budget (`budget.*`), and a stop, a lost lease or a spent budget
ends the loop between steps. An attempt that was cut off is not recorded: the
next claim makes it again. A provider outage hands the card back without
costing an attempt, as for any worker.

## Classification and fingerprints

Each hunk of the resolved file is located between the common text before it and
the common text after it. When the model edited that common text, the last
three lines before the hunk and the first three after it anchor it, fewer when
an edit falls among them. A hunk that cannot be placed is `other`. Indentation,
trailing space and blank lines do not count. Each hunk is one of:

| Choice | The hunk's text is |
| --- | --- |
| `both` | the current side, then the incoming side |
| `both-incoming-first` | the incoming side, then the current side |
| `current` | the current side: the pull request head's, as checked out before the merge |
| `incoming` | the incoming side: the base's |
| `mixed` | made at least half of lines from the two sides, but none of the above |
| `other` | anything else, a hunk that could not be placed, or a deleted file |

The fingerprint is the first eight hex characters of the sha256 of the sorted
per-hunk choice vector: one `file#hunk=choice` entry per hunk. A `mixed` or
`other` hunk carries a short digest of its text in its entry, so two different
mixes have different fingerprints. A note summarises each file as its hunks'
common choice, `both` for either order, or `mixed` when its hunks differ.

## Strategies

The plan for attempt `n` lists variations in a fixed order and picks the first
whose predicted fingerprint no earlier attempt produced. Belai predicts it by
classifying the text each choice would make, so two variations that make the
same text (keeping both sides of a hunk whose current side is empty is the
incoming side) are one variation.

1. `kanban.conflicts.prefer` everywhere: `both` by default, the current side
   first.
2. The next order of keeping both: both, incoming first, everywhere.
3. For the files the last failed check named (a build, lint or test location
   such as `pkg/a.go:12:` in its output, or the file that held markers or
   duplicate imports), current there, then incoming, then both in the other
   order, with the preference elsewhere. The output is used only to test which
   of the conflicted paths it names; none of it is kept.
4. Current everywhere, then incoming everywhere.
5. One file at a time, named files first: each other choice in that file.
6. One hunk at a time: each other choice in that hunk.
7. The named files keeping the preference and every other file current, then
   incoming.

When every planned variation was tried, the strategy is free: the model is asked
for a mix of its own that differs from each earlier attempt in at least one
hunk, and a repeat is still refused.

## Checks

In this order, stopping at the first failure:

| Check | Fails when | Result |
| --- | --- | --- |
| Markers | a conflicted file holds more opening, base or closing markers than either side had | `markers left` |
| Duplicate imports | a conflicted file imports something twice that neither side did (Go, TypeScript and JavaScript, Python, Rust, Java and Kotlin, C#) | `duplicate imports (<file>)` |
| Build | a build check exits non-zero | `build failed (<command>)` |
| Lint | a linter reports more findings than on the head | `lint failed (<linter>: <count>)` |
| Tests | a detected suite (or `tests.command`) exits non-zero | `tests failed (<suite>)` |

The resolution is staged and written as a tree before the build runs, so what
the checks run on is what the merge commit holds, and nothing a build writes
afterwards gets in.

Build checks come from a fixed marker-file table (`internal/builddetect`), argv
only, never a shell string:

| Marker | Check | Command |
| --- | --- | --- |
| `go.mod` | `build-go`, `build-go-vet` | `go build ./...`, `go vet ./...` |
| `Cargo.toml` | `build-cargo` | `cargo build` |
| a `.sln`, `.slnx`, `.csproj` or `.fsproj` at the root | `build-dotnet` | `dotnet build` |
| `gradlew` and a Gradle build or settings file | `build-gradle` | `./gradlew --no-daemon -q classes` |
| `mvnw` and `pom.xml` | `build-maven` | `./mvnw -q -B compile` |
| `tsconfig.json`, with TypeScript installed in `node_modules` | `build-tsc` | `node_modules/.bin/tsc --noEmit` |
| a `build` script in `package.json`, with `node_modules` present | `build-node` | `npm run build`, `yarn run build` or `pnpm run build` |

A wrapper or an installed binary must be a regular file, not a link. Belai never
installs dependencies, so the TypeScript and Node checks run only where
`node_modules` is already in the checkout. Linters come from `internal/lintdetect`
([Lint](lint.md#detection)) and test suites from `internal/testdetect`
([Testing](testing.md#detection)).

Every check runs through the same runner as the test pass and gate
verification: no shell, the user's permission rules, the OS sandbox and the
scrubbed environment. A check that a deny or ask rule stops is not a failed
attempt: the card goes to `blocked` with a note naming the check, because only a
person can change the rule. A check whose program cannot start (not installed
on the host) is skipped and named in the pull request comment as not run, as
are a build that already failed on the head and a linter that could not report
there. The exit code decides a check; only the finding count of a linter is read
from its output.

## The ledger

Each attempt appends one note to the card, composed by Belai from identifiers
only. No model text or tool output is ever part of it:

```
conflict attempt N/5 <fingerprint8>: <file>=<both|current|incoming|mixed|other>[, ...] -> <passed|build failed (<command>)|lint failed (<linter>: <count>)|tests failed (<suite>)|markers left|duplicate imports (<file>)|repeat of attempt M>
```

- At most 12 files are listed, then `+K more`, and fewer when the note would pass
  the board's note limit.
- A path is listed as git names it when it is made of letters, digits and
  `. _ @ + / -`, has no `..` segment and is at most 120 characters. Any other
  path is listed as `file-<8 hex>`, the hash of its bytes, so a file name can
  never break the separators or shape the note.
- A round ends with `conflicts resolved on attempt N, pushed <sha12>`, or
  `conflicts resolved on attempt N, not pushed: <reason>` for a fork or with
  `agents.publish` off, or `conflicts unresolved after 5 attempts`.
- A new round on the same commits starts with `conflict retry: round R` (see
  [Retrying an unresolved card](#retrying-an-unresolved-card)).

The website's card drawer and Checkpoints inspector read these notes as a table
and ignore every other note.

Beside the notes, the ledger is kept as JSON in the state directory
(`~/.vulnetix/belai/agents/conflicts/<item id>.json`, mode 0600, written by an
atomic rename, a symlinked directory or file refused). It holds the head and
base commits, the round number and whether the round ended, the current round's
attempts with their per-hunk choices, every fingerprint any round tried on those
commits, and the baseline. A claim continues the round from it. A host without
the file (another machine made the earlier attempts) rebuilds it from the
card's notes: the round from the last `conflict retry` note, the current round's
attempts, and the fingerprints of every attempt note on the card. When the head
or the base has moved, the conflicts are new and the ledger starts again at
round 1 with nothing excluded.

## Retrying an unresolved card

An unresolved card waits in `blocked` with `checkpoint:conflicts-unresolved`,
and no worker claims it. A person retries it by clearing the checkpoint and
sending it to the backlog: `belai kanban checkpoint K-xxxxxx reject -to backlog`,
or the same decision on the website. Belai never does this itself, and a claim
of a card that still carries any checkpoint label is refused.

The next claim sees that the last round ended on the same head and base and
starts a new round:

- it writes `conflict retry: round R` (2 for the first retry) to the card;
- attempts are numbered from 1 again, up to `kanban.conflicts.max_checkouts`;
- every fingerprint any earlier round produced on these commits stays excluded,
  so the plan picks variations no round tried, and a resolution that repeats one
  is refused as in any round;
- each brief says which round it is and lists the earlier rounds' fingerprints.

The baseline is kept, because the head is the same. If the pull request's head
or base moved before the retry, there is no new round: the conflicts are new,
and the ledger starts again at round 1.

## Pushing

A passing resolution is committed by Belai as a merge with two parents, the
head and the base commits the attempt started from. Any commit the model made
is left out. The message names the attempt and the strategy:
`Merge <base> into <head>`, then `Resolved by agent belai:conflict-resolver on
conflict attempt N of 5 (strategy: <name>), kanban item K-xxxxxx.`

Then, with `agents.publish` on:

- For a pull request from the same repository, Belai pushes the merge commit to
  `refs/heads/<head>` on `origin` with an explicit refspec and no `+` or
  `--force`, so the forge accepts it only as a fast-forward. When the push fails
  (the head moved while the attempt ran, or the credential was refused), the
  passing attempt is taken out of the ledger and the card goes back to the
  backlog as a failed claim (one of its `kanban.max_attempts`), so the next
  claim fetches the new head and starts again.
- For a fork, Belai does not push to the fork. It pushes the attempt's branch to
  `origin` under its own name (`belai/K-xxxxxx/aN`) and says so in the comment
  and the card's note.

Belai then comments the ledger on the pull request as a table, with what was
pushed and which checks ran or were skipped, and the card goes to `review`
labelled `needs-review` without `merge-conflict` (`kanban.on_success`).

The card's branch (`agent.branch` on the website) names what the review stage
should check out, so a reviewer on any host finds the work:

| Case | The card's branch |
| --- | --- |
| Pushed to a same-repository pull request | the pull request's head, `<head>` |
| A fork, its resolution published to `origin` | the attempt's branch, `belai/K-xxxxxx/aN` |
| Not pushed (`agents.publish` off, or a fork whose publish failed) | the attempt's branch, on this host only |

A card's branch is otherwise always a `belai/` branch, and that rule still
holds for every other name. The pull request's head is accepted only when it is
the `head` of the card's own pull request block, the block names the worker's
`origin` and `fork: false`, and git accepts the name. The reviewer's worktree
then fetches that head from `origin` (into `refs/belai/pull/N/head`) and checks
it out on a new branch of the card's own, so no local branch of the user's is
checked out or moved, and the commit reviewed is what the forge holds now. A
review that commits nothing leaves the card on the pull request's head; one that
commits records its own branch, as any stage does. The model never pushes, opens or comments on a
pull request: the profile has no `PublishBranch` tool, and the worker's Bash
refuses `git push` and `gh pr comment` as for every worker
([Publishing](fleet.md#publishing)).

## The profile

| Field | Value |
| --- | --- |
| `kanban.labels` | `merge-conflict`, from `backlog` |
| `kanban.on_success` | `review`, labelled `needs-review`, dropping `merge-conflict` |
| `kanban.conflicts.max_checkouts` | `5` |
| `kanban.conflicts.prefer` | `both` |
| `kanban.max_attempts` | `3` claims, each running a full round of checkouts at most |
| `workspace.isolation` | `worktree`, with no `publish`: Belai pushes |
| `autonomy` | `autonomous`, with a cost and pass budget |

Its prompt asks it to prefer keeping both sides and then remove what became
redundant, to keep both sides' intent, to follow a variation it is told to try,
never to delete or weaken a test or a lint rule, and to edit outside the
conflicted files only when a build or lint error from the merge requires it.
See [`kanban.conflicts`](agent-profiles.md#worker-profiles) for the fields.

Code: `internal/mergefix` (hunks, classification, fingerprints, the ledger,
duplicate imports, markers, the plan, the card), `internal/builddetect`, and
`internal/fleet/conflicts.go` (the loop).
