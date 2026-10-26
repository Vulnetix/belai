# Session measures

A session records what it cost, what it checked and what it committed, so the
Software Factory can add the figures up per outcome and per model. Every figure
is measured by the harness from a process exit code, a git object or a
provider's usage block. None of it comes from model text.

The code is in `internal/sessionmeasure`. Four records carry the figures:

- Cache tokens and cost on each assistant entry.
- A `check_run` entry for each build, test, lint or gate run.
- A `commit` entry for each commit the harness makes.
- A `rollup` on the call that ends the session.

## Assistant entries: cache tokens and cost

An `assistant` entry's `meta` carries the usage of the model call that produced
it:

| Key | Value |
| --- | --- |
| `prompt_tokens`, `completion_tokens`, `total_tokens` | As reported by the provider |
| `cache_read_tokens`, `cache_write_tokens` | The parts of the prompt the provider read from, or wrote to, its cache. 0 when the provider reports none |
| `cost_micro_usd` | Integer millionths of a dollar. Absent when the model has no list price |

The cost is `budget.CostFor`, which prices the call with `budget.Price.MicroUSD`
in this order:

1. A price the Software Factory sent with the work wins. A worker takes it from
   its dispatch, and it is used for the turn it was sent for.
2. A local provider (`ollama`, `llama-server`, `localinfer`, `lmstudio`) is
   free: `cost_micro_usd` is 0.
3. The built-in table in `internal/budget/cost.go` prices the model families
   Belai ships defaults for.
4. A model with no price gets tokens and no `cost_micro_usd` key. The server
   keeps its tokens and shows its cost as not priced.

`provider` and `model` are the model that made the call. The TUI writes them
from the message, and `internal/turnlog` from `Log.SetModel` or the footer facts
of the turn (`turn_state` carries `provider` and `model`). Headless, rc, fleet
and ACP sessions write assistant entries through `turnlog`, which adds up the
usage of the model calls since the last entry and writes it on the next one.
Subagent and classifier calls are not main-model calls and are not counted.

## `check_run` entries

One entry for each build, test, lint or gate run that exited:

```json
{"type":"check_run","role":"system","timestamp":1790000000000,
 "meta":{"kind":"test","program":"go","subcommand":"test","exit_code":0,"duration_ms":4210,"ok":true}}
```

| Key | Value |
| --- | --- |
| `kind` | `build`, `test`, `lint` or `gate` |
| `program` | The program that ran: `go`, `cargo`, `npm`, `yarn`, `pnpm`, `make`, `pytest`, `mvn`, or the base name of a suite's own command |
| `subcommand` | The subcommand, script, target or goal that picked the check: `test`, `vet`, `build`, `lint`, `test:unit`. Empty when there is none |
| `exit_code` | The process exit code. -1 for a run killed at its timeout |
| `duration_ms` | Wall time of the run |
| `ok` | `exit_code` is 0 |

The entry has no `content`.

### Where they come from

| Source | Kind | Written by |
| --- | --- | --- |
| The post-end test pass (`internal/testpass`), in the TUI, headless, rc and ACP | `test` | `testpass.Config.Checks` |
| A fleet worker's gate verification (`internal/fleet/verify.go`) | `gate` for a run that decides a card's gate, `test` for the regression suites | `Worker.itemResultsAt` |
| A fleet worker's conflict resolution checks (`internal/fleet/conflicts.go`) | `build`, `lint`, `test` | `Worker.itemResultsAt` |
| A Bash tool call, and a `!cmd` line in the TUI or the website's shell | The command's class | `sessionmeasure.FromBash` |

A run that never executed (a deny or ask rule refused it, the binary is
missing, no sandbox) is not a check and writes nothing. The quality and lint
sweeps run outside any session and write nothing. The baseline runs of the
conflict resolver, made before its first turn, write nothing either.

A fleet worker writes its checks to the session of the claim it is on. When no
turn has run for the claim, nothing is written.

### The classifier

`sessionmeasure.Classify` reads an argv and `sessionmeasure.ClassifyLine` a
shell line. The line is parsed as bash (`internal/shellsafe`), so a program
named inside an `echo`, a string or a comment is not a check, and a wrapper
such as `env`, `sudo`, `time` or `sh -c` is looked through. A line that chains
several checks, such as `go build ./... && go test ./...`, is recorded once as
its last check, with the exit code and the duration of the whole line.

| Program | Build | Test | Lint |
| --- | --- | --- | --- |
| `go` | `build`, `install` | `test` | `vet` |
| `cargo` | `build`, `b`, `check`, `c`, `install` | `test`, `t`, `nextest` | `clippy`, `fmt --check` |
| `npm`, `yarn`, `pnpm` | Script `build*` or `compile*` | `test`, or script `test*` | Script `lint*` |
| `make` | No target, `all`, `install`, `compile`, `build*` | `check`, `test*` | `vet`, `lint*` |
| `pytest` | None | Always, including `python -m pytest` and `uv run pytest` | None |
| `mvn`, `mvnw` | `compile`, `package`, `install` | `test`, `verify`, `integration-test` | `checkstyle:check`, `spotless:check`, `pmd:check`, `spotbugs:check` |

A node script is run with `run`, `run-script`, `yarn workspace NAME` or, for
`test`, directly. The script name is the `subcommand`.

The classifier never returns `gate`. A caller that knows a run decided a gate
says so, and the plan name `gate:<id>` does in the fleet worker.

### How the exit code is read

For a testrun result the exit code is the process's, and a timeout is -1. For a
Bash result, the tool appends `exit status N` after the output of a command that
failed and a timeout line after one it killed. `sessionmeasure.BashExit` reads
the last such line, so a result with neither exited 0. The whole result is read
before it is cut to the 32 KiB a transcript keeps. A result the harness withheld
says nothing about the command and writes no entry. A line that was refused or
never started writes none either.

## `commit` entries

One entry for each commit the harness makes:

```json
{"type":"commit","role":"system","timestamp":1790000000000,
 "meta":{"sha":"0123456789abcdef0123456789abcdef01234567",
         "author_name":"Pix [bot]","author_email":"security@vulnetix.com",
         "committer_name":"Pix [bot]","committer_email":"security@vulnetix.com",
         "trailers":[{"key":"Signed-off-by","value":"Pix [bot] <security@vulnetix.com>"},
                     {"key":"Co-authored-by","value":"Chris L <chris@vulnetix.com>"}],
         "branch":"belai/k-0363e1"}}
```

| Key | Value |
| --- | --- |
| `sha` | The full commit id |
| `author_name`, `author_email`, `committer_name`, `committer_email` | As git stored them |
| `trailers` | The key and value of each trailer of the message, in order, up to 32. An empty list when there are none |
| `branch` | The branch HEAD is on. Empty on a detached HEAD |

The entry is read back from git after the commit, not built from the message the
harness sent, so it shows the identity git used and the trailers a commit hook
added. It is written by:

- `fleet.Workspace.Commit`, the harness's commit of what a worker left, and the
  conflict resolver's merge commit, through `Workspace.OnCommit`.
- The TUI's auto-commit after a completed goal.
- The auto-commit of a remote-control session.

A commit the model makes itself with `git commit` is not recorded.

## The finish rollup

The call that moves a session to History, `POST /sessions/{id}/end`, carries a
`rollup` next to the reason. The syncer adds it up from the session's JSONL when
the session ends, whether the host closes it, another session replaces it or
`/sync backfill` uploads it.

```json
{"reason":"completed",
 "rollup":{"active_ms":812000,"wait_ms":45000,"tool_calls":132,
           "check_counts":{"build":3,"test":5,"lint":1,"gate":0},
           "files_changed":9,"additions":410,"deletions":73,
           "models":[{"provider":"anthropic","model":"claude-sonnet-4-5",
                      "prompt_tokens":120000,"completion_tokens":9000,
                      "cache_read_tokens":80000,"cache_write_tokens":10000,
                      "cost_micro_usd":642000,"priced":true}]}}
```

| Key | Value |
| --- | --- |
| `active_ms` | The time agent turns ran (`turn_state` `duration_ms`), less the time they spent waiting for a person |
| `wait_ms` | The time from each `ask` to its `ask_answer`. Asks that overlap count once. A plan review does not block the agent and is not counted. An ask nobody answered has no wait |
| `tool_calls` | The number of `tool` entries, subagent calls included |
| `check_counts` | The `check_run` entries by kind. Null for a transcript no measuring host wrote (see below) |
| `files_changed` | Distinct paths the session's tools changed |
| `additions`, `deletions` | Lines the changes added and removed, summed over every change, so a line edited twice counts twice. The counts are `filediff.Stat` of each change, as written into `meta.diff` |
| `models` | One row per provider and model, sorted by provider then model |

A model row adds up the model's assistant entries. `priced` is true when every
one of them carried `cost_micro_usd`, and then `cost_micro_usd` is their sum.
Otherwise `priced` is false and `cost_micro_usd` is null: the row keeps its
tokens and does not report a cost that undercounts. A local model is priced at 0.

`check_counts` is null when the transcript holds no sign that a measuring host
wrote it: no `check_run` or `commit` entry and no assistant entry with
`cache_read_tokens`. A count of 0 on such a transcript would claim a
measurement nobody took. A session of a measuring host that ran no check has
`check_counts` with every count 0.

A session whose file cannot be read ends without a rollup, as an older host's
does.

## Old hosts

A host that predates a record sends none of it. The server stores null for the
figure and shows it as not recorded yet, never as 0.

## Limits

- Cost is a list price, so a plan, a discount or a cache tier the table does
  not know changes what the provider charges.
- A model call that returns neither text nor a tool call writes no assistant
  entry, so its usage is added to the next call's.
- An ACP session records the last run of the post-end test pass, not each pass
  of a fix loop.
- A command piped into another (`go test ./... | tail`) has the exit code the
  shell reports for the pipeline.
