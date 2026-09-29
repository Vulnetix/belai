# Test suites and the post-end test pass

Belai detects a repository's test suites, tells the model how the project
tests itself, and can run those suites when work ends. A pass gets a short
report, and a failure gets a bounded diagnose-and-fix loop. The pass is off
until the user turns it on, like [auto-commit](architecture.md#per-goal-auto-commit).

- [Detection](#detection)
- [What the model sees](#what-the-model-sees)
- [Settings](#settings)
- [When the pass runs](#when-the-pass-runs)
- [Running the suites](#running-the-suites)
- [Pass: the report](#pass-the-report)
- [Failure: diagnose and fix](#failure-diagnose-and-fix)
- [Surfaces](#surfaces)
- [Security](#security)
- [Edge cases](#edge-cases)

## Detection

`internal/testdetect` turns the repository map's flat test commands into
suites. It reads marker files and package.json script names only, never file
bodies or prose, so the result is harness-computed fact.

| Marker | Suite | Command | Framework | Test files |
| ------ | ----- | ------- | --------- | ---------- |
| `justfile` with a `check` or `test` recipe | `just-check`, `just-test` | `just check` / `just test` | `just` | none |
| `Makefile` | `make-test` | `make test` | `make` | none |
| `go.mod` | `go` | `go test ./...` | `go` | `*_test.go` |
| `Cargo.toml` | `cargo` | `cargo test` | `cargo` | none |
| `pyproject.toml` | `pytest` | `pytest` | `pytest` | none |
| `package.json` with a `test` script | `node` | `npm test` | `jest`, `vitest` or `mocha` from a config file, else empty | none |

Rules and edge cases:

- A command that does not resolve to a real suite is skipped. `npm test` in a
  package with no `test` script is not a suite.
- A node framework is named from the presence of a config file (`jest.config.*`,
  `vitest.config.*`, `.mocharc.*`) and never from its contents. Jest wins over
  vitest, and vitest over mocha, when several exist.
- Only the script name is read from package.json. A script body is never
  read, so a hostile script cannot reach the map.
- Suites are de-duplicated by name and kept in the order the map lists them,
  so a `justfile` recipe comes before the ecosystem command.

### Scoping to changed paths

With `tests.scope` set to `affected` (the default), Go suites are narrowed to
the packages that own changed `.go` files: `go test ./internal/x/...`.

- Every other ecosystem runs its full command, because Belai cannot map a
  changed path to a test target there without reading code.
- A change to `go.mod`, `go.sum` or `go.work` means the dependency graph moved,
  so the whole suite runs.
- Changed paths with no Go file (docs, config) leave the suite unscoped.
- A changed file in the module root scopes to `./...`, since the root package
  imports most of the tree.
- `tests.scope` set to `full` never scopes.

## What the model sees

The stable part of the repository map, in the system block, gains a `tests:`
line: `tests: go [go] go test ./... files *_test.go`. It carries the suite name,
framework, argv and test-file convention, so a model that writes code writes
the tests the way the project runs them.

The volatile part, on the per-turn directive, carries only the result of the
last pass: `last test run: pass, 2 suites`. It is a verdict and a count. No
test output, name or message ever appears there. Every string is passed through
`sanitize.Sanitize` first.

## Settings

The `tests` block is read from the user's own layers. A repository's project
layer can only tighten it.

| Key | Values | Default | Meaning |
| --- | ------ | ------- | ------- |
| `tests.post_end` | `off`, `goal`, `goal_plan`, `session` | `off` | When the pass runs; see [When the pass runs](#when-the-pass-runs) |
| `tests.command` | argv list | none | Runs this command instead of the detected suites, unscoped |
| `tests.scope` | `affected`, `full` | `affected` | Narrow Go suites to changed packages |
| `tests.on_fail` | `off`, `diagnose`, `fix` | `fix` | What a failing run starts |
| `tests.max_fix_passes` | integer | 3 | Ceiling for the fail branch |
| `tests.timeout_seconds` | integer | 300 | Bound on one suite run |
| `tests.report` | bool | true | Fast-model report on a pass; false uses the harness line |

Rules:

- **Unknown values fail closed.** An unrecognised `post_end` is off, and an
  unrecognised `on_fail` is `off`, so a typo never widens the pass to editing
  files. An unrecognised `scope` is `affected`.
- **A project layer may only tighten.** A repository's `.vulnetix/settings.json`
  may set `tests.post_end` to `off`, and lower `max_fix_passes` and
  `timeout_seconds`. It can never turn the pass on or set `command`, `scope`,
  `on_fail` or `report`, because a cloned repository must not choose commands
  that run on the user's machine or widen the fail branch. The resolver adds a
  note when it drops a key.
- **`tests.command` is an argv, not a shell line.** It runs without a shell, so
  `&&`, pipes and globs do nothing. Use a script or a `just` recipe for that.
- **`/settings` shows each key with the layer it came from**, and the report
  toggle cycles on, off and unset.

## When the pass runs

| `tests.post_end` | After a completed goal | After a completed plan | At session end |
| ---------------- | ---------------------- | ---------------------- | -------------- |
| `off` | no | no | no |
| `goal` | yes | no | no |
| `goal_plan` | yes | yes | no |
| `session` | yes | yes | yes |

- **Only a completed goal triggers.** A turn that ends `GOAL_PARTIAL`,
  `GOAL_NOT_STARTED`, a stopped goal or a plain agent turn does not.
- **Plan mode never runs it.** Plan mode changes nothing, so there is nothing
  to test.
- **Session end needs an edit.** The session-end pass runs only when the
  session changed a file since the last pass (in the TUI) or the working tree
  has changes (headless). An idle session never spends a suite on exit.
- **Passes never overlap.** A pass in flight blocks a second one.

## Running the suites

`internal/testrun` runs each suite:

- **Fixed argv, no shell.** The argv comes from the detection table or the
  user's `tests.command`. It is never taken from a model.
- **Exit code decides.** Pass is exit 0. A run that prints `FAIL` and exits 0
  is a pass, and one that prints `PASS` and exits 1 is a fail. No model is
  asked whether a run passed.
- **Permission rules apply.** A deny rule matching the command refuses it
  (status `denied`). An ask rule skips it (status `needs_approval`), because
  the pass has nobody to ask; add an allow rule to run it. Neither counts as a
  pass.
- **Sandbox, scrubbed environment, own process group.** The command runs under
  the call's OS sandbox policy with `proc.ScrubbedEnv`, so a secret in the
  parent environment does not reach a test process.
- **Bounded.** `tests.timeout_seconds` bounds each suite (status `timeout`,
  exit code -1), and output is kept head and tail up to 64 KiB.
- **A missing binary is an error, not a pass.** A command that cannot start
  (status `error`) never counts as passing, and never enters the fail loop,
  since there is nothing for a model to fix.

The results are one of `pass`, `fail`, `timeout`, `denied`, `needs_approval` and
`error`. Every suite must pass for the pass to pass, and an empty result is not
a pass.

## Pass: the report

On a pass the harness writes a short report. With `tests.report` on (the
default) the fast-tier `test_report` role writes it.

- The role is tool-less. It sees harness facts (trigger, suite names, statuses,
  exit codes, durations, the number of fix passes) and a bounded excerpt of the
  output that passed the gate.
- The excerpt is process output, so it is sanitised and classified as
  `KindProcess` under the effective posture before the role sees it. Withheld
  output is dropped and the role still reports on the facts.
- Any failure (no classifier, a transport error, an empty reply) falls back to
  the harness-composed line, for example `Tests passed: go pass, node pass in
  2s.` A weak or absent fast model never costs the pass its result.
- The reply is flattened to one paragraph and capped at 600 characters.
- The role's decision appears in the role-manager record and the activity feed
  as `test_report`, with the model that wrote it. It can be routed like any
  other fast use case.

## Failure: diagnose and fix

A failing run, with `tests.on_fail` not `off`, hands the failure to a main-model
agentic loop, bounded by `tests.max_fix_passes`.

- **`fix`** runs a goal-mode turn on the full surface with no goal-contract
  draft: the harness prompt is the objective. It asks for a root-cause fix,
  a re-run of the failing command, and forbids deleting, skipping or weakening
  a test to make it pass unless the test itself is wrong. After each fix turn
  the harness re-runs the suites. The loop stops at green, at the budget, on an
  error, or when the user cancels.
- **`diagnose`** runs one turn on the read-only plan surface (no `Bash`, no
  editing tools) that finds the root cause and proposes the smallest fix. It is
  one turn because nothing changes, so a re-run would repeat itself.
- **The failing output rides only as an attachment**, gated as `KindProcess`.
  The harness prompt holds only facts (suite names, statuses, exit codes and the
  commands). If the gate withholds the output, the prompt says so and the loop
  runs the command itself under the normal `Bash` rules.
- **Every gate still applies.** The loop's tool calls go through the session's
  permission rules, ask gate, classifier and sandbox. A session that cannot ask
  never widens.
- **A refusal is not a failure to fix.** If no suite actually ran (all denied,
  needing approval, or errored), the loop is not entered.
- **Edits made by the loop are ordinary edits.** In the TUI, per-goal
  [auto-commit](architecture.md#per-goal-auto-commit) commits a completed fix
  turn like any other goal.

## Surfaces

| Surface | Trigger | Fail loop |
| ------- | ------- | --------- |
| TUI | Completed goal, completed plan, and quit at session level | A visible turn on the main session, then a re-run |
| Headless (`-prompt`) | Completed goal, or run end at session level with a changed tree | The same session, then a re-run |
| ACP | Completed goal only | A turn streamed to the editor; permission asks go to the editor |

- **TUI.** The pass runs off the UI loop. A failing run starts an ordinary
  visible turn (goal mode for a fix, plan mode for a diagnosis), and that turn's
  end re-runs the suites, so you watch the loop work. A turn that errors ends the
  loop. At session level, the first quit runs the pass and stays open to show
  the result; quitting again exits at once and cancels the suites.
- **Headless.** The report and result go to stderr, so stdout stays the reply.
  The exit code of the run is unchanged by the pass, so a failing suite does
  not fail a script that only wanted the reply; read the stderr line to gate on
  it. A `-plan` run changes nothing and never triggers a pass.
- **ACP.** An editor has no session-end moment, so only a completed goal
  triggers. Results stream as agent messages, and nothing but protocol
  messages is written to stdout. Cancelling the prompt cancels the pass.
- **Guardrails off** applies `posture.AllIgnore()` on every surface: the gate
  only sanitises, and the sandbox is off. Sanitising always runs.

## Security

- Suite argv comes from the harness or the user, never a model.
- Pass or fail is the exit code, never a model's opinion.
- Test output is arbitrary text: it is sanitised and classified as
  `KindProcess` before any model sees it, and reaches the model only as an
  attachment, never in a prompt, system block or directive.
- The fast report role and the repository-map facts carry harness facts and
  gated text only. The map line carries a verdict and a count.
- The project layer can turn the pass off and lower its budgets. It can never
  turn it on or choose a command.
- The suites run under the user's permission rules and the OS sandbox with a
  scrubbed environment.

## Implementation map

| Package | Function | Role |
| ------- | -------- | ---- |
| `internal/testdetect` | `Detect` | Builds suites from the repository map's test commands, scoped to changed paths |
| | `Rescope` | Re-scopes suites to a new changed-path set; Go maps to package directories, the rest stay full |
| | `Suite.ScopedCommand` | The argv to run: `go test ./pkg/...` for a scoped Go suite, else the full command |
| | `CommandStrings` | The flat command list `repomap.Commands.Test` is derived from |
| `internal/testrun` | `Run` | Runs one argv: permission check, sandbox, scrubbed environment, exit code decides |
| | `BuildPlan` | Picks what to run: the override alone, or the suites scoped by `tests.scope` |
| | `RunPlan` | Runs each entry in order and stops on a cancelled context |
| | `AllPassed` | True only for a non-empty result where every suite passed |
| | `Subject` | The string a `Bash` permission rule is matched against |
| | `Result.OK` | True for a `pass` result |
| `internal/testpass` | `Should` | Maps `tests.post_end` and a trigger to yes or no |
| | `RunOnce` | One run of the suites and the fix request it implies; the TUI's primitive |
| | `Run` | The blocking loop headless and ACP use: run, then report or fix and re-run |
| | `NewGate` | The classification gate for process output, level checked before the classifier |
| | `FixPrompt`, `FixAttachments`, `FixInput` | The harness-composed prompt, the gated output attachment and the agent turn for a fail-branch pass |
| | `SessionFixer` | A fixer that runs each request as a turn on an existing session |
| | `Outcome.Line`, `Outcome.Summary` | The one-line result and the repo-map `last test run` fact |
| `internal/rolemanager` | `DecideTestReport` | The fast-tier report, with the harness-composed fallback |
| | `ComposeTestReport`, `BuildTestReportPayload`, `CleanTestReport` | The fallback line, the tool-less payload and the reply cleaner |
| `internal/headless` | `ShouldPostEnd`, `RunPostEnd` | The CLI and ACP entry: trigger gating, then a pass with the run's own session |

## Edge cases

- No suite detected and no `tests.command`: the pass is skipped with `tests: no
  test suite detected`.
- A failing run that cannot be fixed within `tests.max_fix_passes` ends with the
  failing result, no report.
- Two suites where one fails: the pass fails, and the fix request lists only the
  failing suites.
- A cancelled context (a quit, an editor cancel) stops the loop and any running
  suite.
- The result shown by `last test run:` is the last completed run, and a skipped
  pass leaves it alone.
- A dirty tree from before the session widens `affected` scoping, since scoping
  reads the working tree's changes.
