# Finish review

**Status:** new.

Before a goal is accepted as complete, the harness reads the working tree itself
and may send the completion back once or twice with a short list of what it found.
It looks for four things a model's "done" does not prove: scratch files the goal
left behind, a test that lost lines or gained a skip marker to get green, source
code that changed after the last passing check, and any part of the tree the
review could not read.

No model is asked whether a file is scratch, a test was weakened or a check ran.
The review reads file names, line counts and fixed marker tables, and a finding is
a closed kind with a path from a plain alphabet and a count. It approves nothing,
deletes nothing and changes no file, index or ref: the remedy is the model's own
ordinary tool calls, under the usual permissions, hooks and sandbox.

- It is on by default and costs one `git status` and the diff of the changed files
  at the end of a goal, plus one more at the start.
- `/finish-review` shows it and switches it for the session. `-no-finish-review`
  and `BELAI_FINISH_REVIEW=0` switch it off for a run.
- Code: `internal/finishreview` (the checks and the saved baseline),
  `internal/agent/finishreview.go` (the gate in the goal pass loop),
  `internal/config/finishreview.go` (settings), `internal/workdiff` (the read-only
  git collection it shares with `/diff`) and `internal/tui/finishreview_cmd.go`.

## When it runs

The goal pass loop accepts a `GOAL_COMPLETE` verdict only after the verification
gate (see [Role Manager](role-manager.md)). The finish review runs right after that
gate and the open-todo check, at both places the loop accepts a completion.

- With findings and rounds left, the loop adds one sealed harness directive that
  lists them and runs another pass. The count of rounds used is the loop's own.
- With findings and no round left, the goal is accepted. The findings are shown as
  a warning, and the directive for the final report names them, so the report says
  which are intended.
- With no findings, nothing changes.

A request the scale job rated simple, and a turn whose deliverable is a decision,
skip the review, as they skip the verification pass. Agent, plan and code mode turns
have no completion verdict and no review.

## What it checks

| Kind | Meaning |
| --- | --- |
| `scratch` | An untracked file, new since the goal started, whose base name matches the scratch table or one of your patterns. |
| `test_removed` | A test file that lost more lines than it gained while source code also changed. |
| `test_skipped` | A test file that gained a skip or focus marker while source code also changed. |
| `unverified` | Source code changed and no passing, non-inspection command ran after the last change. The finding names the detected suites. |
| `unreviewed` | Part of the tree was not read: more changed files than the diff limit (300), a path a `Read` deny rule hides, a file too large to diff or unreadable, or a directory that is not a git repository. |

The scratch table is fixed. A base name is matched case-insensitively against
`*.orig *.rej *.bak *.tmp *.log nohup.out scratch* debug*.go debug*.py debug*.js`.
Files git ignores never appear in the working tree status, so an ignored log is not
scratch. A project can add patterns and cannot remove one.

A test file is recognised by the usual names (`_test.go`, `test_*.py`, `*.test.ts`,
`*.spec.js`, `*Test.java`, `*_spec.rb`) or by sitting under a `test`, `tests`,
`__tests__` or `spec` directory. A skip marker is one of a fixed list (`t.Skip(`,
`xit(`, `it.skip(`, `.only(`, `@Disabled`, `@pytest.mark.skip`, `#[ignore]` and
similar). Only the count of markers in the old and the new text is compared, and
the text itself never leaves the review.

A report lists at most 20 findings, most serious first, then by path; the rest are
counted. A path appears in a finding only if it is made of letters, digits and
`. _ - / @ +`. A file with any other name is counted ("3 new file(s) with unusual
names") and never echoed, so a file name cannot carry text into a directive.
Anything under `.git` or `.vulnetix` is Belai's and git's own state and is not
reviewed.

An `unreviewed` finding alone never sends a completion back: there is nothing for
the model to fix. It is shown to you and put in the report, and the goal ends.

## The baseline

When a goal starts, the harness records which files were already changed, so a
file you left in the tree is not called scratch. The baseline is the list of
changed paths, saved to `<state dir>/finish_review/<goal id>.json` (mode 0600 in a
0700 directory, written through a temporary name and a rename, never through a
symbolic link). The state directory is hidden from the OS sandbox, so a command
cannot edit it. A goal that resumes reads the file its first run saved.

A baseline that is missing, unparsable, over 1 MiB, from another version,
incomplete or out of shape is the empty baseline, which is the stricter reading:
every untracked file counts as new. A start in a tree with more than 300 changed
files, or outside a repository, records no baseline.

## Settings

The block is `finish_review`. A malformed value is refused when the settings load.

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `finish_review.enabled` | `true` | boolean | Whether the gate runs. |
| `finish_review.max_rounds` | 2 | 0 to 5 | How many times a completion may be sent back. 0 never sends one back; findings are still named in the report. A review whose only finding is `unreviewed` never uses a round. |
| `finish_review.scratch_patterns` | none | up to 32 | Extra scratch patterns. A pattern with no slash matches a file's base name, one with a slash matches the whole relative path. Each is 1 to 80 of letters, digits and `. _ - / @ + * ?`, with at least one character that is not a wildcard. |

The review only adds scrutiny, so a project settings file can only tighten it. It
cannot switch the gate off and cannot lower `max_rounds`; it can raise `max_rounds`
(up to 5) and add patterns, up to the cap of 32. A project value that would loosen
the gate is dropped with a note. Your own layers set any key.

| Switch | Effect |
| --- | --- |
| `/finish-review` or `/finish-review status` | Show whether the gate is on, the rounds, the number of extra patterns and where the value came from. |
| `/finish-review on`, `/finish-review off` | Switch the gate for this session. Held in memory, re-applied over every settings reload and never written to a file. A web session cannot send it. |
| `-no-finish-review` | Switch the gate off for this run. |
| `BELAI_FINISH_REVIEW` | A false value (`0`, `false`) switches the gate off for this run. Any other value is ignored, so it cannot switch on a gate your settings turned off. |

A repository can switch none of these.

## Surfaces

The review lives in the goal pass loop, so it runs wherever a goal runs: the
terminal UI, `-prompt` runs, `belai acp`, remote-control sessions and fleet
workers. Each has the gate and the settings above. `/finish-review` is a terminal
UI command.

## What the model never decides

- Whether a file is scratch, a test was weakened, a check ran or the tree was read.
- Whether a finding is waived. After the rounds are spent the goal is accepted and
  the findings stay in the report. The review never blocks a goal for ever.
- Anything about its own baseline or settings: the baseline is in the hidden state
  directory, and a project file can only tighten the settings.

A finding reaches the model only as a sealed harness directive built from a fixed
template, the closed kinds, counts and paths from the plain alphabet. It is not a
tool result, and it is never in the system block. Nothing from the review goes to
telemetry, the audit log or a classifier. A `Read` deny rule hides a file from the
review exactly as it does from `/diff`: the file is not read, and the review says
that part of the tree was not seen.

A critic model that judges the findings is not part of this change.
