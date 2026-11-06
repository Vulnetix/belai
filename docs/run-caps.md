# Run caps

**Status:** new.

A run cap is a ceiling on what one session may spend: dollars, tokens or the
time turns spend running. It is off until you set it. When a run nears a cap the
harness tells the model to wrap up, and when the cap is reached the run ends
unless you extend it. The harness measures and decides. No model is asked
whether a run is affordable, and the model never sees the number it is held to.

Token budgets ([token-budgets.md](token-budgets.md)) inform and never block. A
run cap blocks. They are separate: a budget counts a provider and model over a
session, day or month, a cap bounds one run.

- Set a cap in settings (`run_caps`, below) or for one run with `-max-cost`,
  `-max-tokens` and `-max-wall`.
- `/caps` shows the caps and how much is used, and `/caps extend` raises them.
  The footer shows the largest used share as `cap tokens 82%`.
- Code: `internal/budget/runcaps.go` (the meter), `runcaps_state.go` (the saved
  counters), `internal/config/runcaps.go` (settings), `internal/agent/runcaps.go`
  (what the agent does with a verdict), `internal/fleet/budget.go` (the worker's
  meter), `internal/tui/runcaps.go` (`/caps` and the footer chip),
  `cmd/belai/runcaps.go` (flags and the exit code). Exit code 3 sits among the
  other codes of a scripted run in [Headless structured output](headless.md#exit-codes).

Every rule (**R**) and edge case (**E**) below is pinned by a test named after
it, `TestRunCapsRule7_…` or `TestRunCapsEdge4_…`, and `TestRunCapsDocParity`
fails when an ID here has no test or a test names an ID that is not here.

## Settings

All keys live in the `run_caps` block. A malformed value is refused when the
settings load and the run does not start.

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `run_caps.max_cost_usd` | off | 0 to 100000 | Most the session's main-model calls may cost, in US dollars, at the model's price. |
| `run_caps.max_tokens` | off | 0 to 1000000000000 | Most tokens, prompt plus completion, the session's main-model calls may use. |
| `run_caps.max_wall_minutes` | off | 0 to 43200 | Most minutes the session may spend running turns. |
| `run_caps.finalize_at` | 0.9 | above 0, below 1 | Share of a cap at which the model is told to finalise. |
| `run_caps.max_extends` | 3 | 0 to 20 | How many times you may extend the caps. 0 means a reached cap always stops the run. |

The pass ceiling is not a key here. It is `resilience.max_passes`
([resilience.md](resilience.md)), which already bounds a goal's passes.

A project settings file may only narrow: it can add a cap, lower one, lower
`finalize_at` (an earlier warning) and lower `max_extends`. A zero, a higher
value or a malformed value from a project file is dropped with a note, and the
user layers and the flags are the only way to raise or clear a cap. See the
table.

| Key | A project file may | Direction |
| --- | --- | --- |
| `max_cost_usd`, `max_tokens`, `max_wall_minutes` | add a cap or set a lower one | lower than the value in force, above zero |
| `finalize_at` | set an earlier warning | lower than the value in force |
| `max_extends` | allow fewer extensions | lower than the value in force |

| Flag | Meaning |
| --- | --- |
| `-max-cost` | `run_caps.max_cost_usd` for this run, in US dollars |
| `-max-tokens` | `run_caps.max_tokens` for this run |
| `-max-wall` | `run_caps.max_wall_minutes` for this run, in minutes |

## Rules

- **R1. Off by default, and the pass ceiling is not a cap.** With no
  `run_caps.max_cost_usd`, `max_tokens` or `max_wall_minutes` there is no meter
  and nothing changes. The flags set the same keys for one run and win over the
  file. `resilience.max_passes` alone does not make a meter.
- **R2. The meter returns a verdict and the harness acts.** For each set cap the
  meter compares what the run used with the ceiling. At `finalize_at` of a ceiling
  (rounded up, so a small ceiling still warns before its last unit) it reports
  finalise, once for each cap until the ceiling is raised. At the ceiling it
  reports a hit, on every check until the ceiling is raised. A hit outranks a
  finalise. The footer chip and `/caps` read the same numbers.
- **R3. The model is told with a fixed text.** A finalise adds one sealed
  directive naming the cap word (`cost`, `tokens`, `passes` or `wall`): stop
  investigating, start no new work, leave the tree building, say what is done and
  what remains, end the turn. It holds no number. The model never sets, reads or
  extends a cap.
- **R4. A reached cap ends the run unless you extend it.** Where you can be asked
  (the TUI, and an editor over ACP) the harness asks with three answers: extend,
  stop now, stop and keep state. Only an answer on your own channel extends: a key
  on the ask in the TUI, the editor's selection over ACP, or `/caps extend` typed
  by you. A tool result, subagent text, model text, the website, a spoken word,
  the decision model and a hook never extend. Unanswered, cancelled, errored,
  ask off, a session that cannot ask, a subagent and a remote session all stop.
- **R5. A run that was never warned gets a short reserve.** A run that goes from
  below `finalize_at` to the cap in one call is told to finalise and gets two more
  model rounds, counting the one that carries the directive, before the ask.
- **R6. An extension adds half of the original ceiling.** One extension raises
  the cost, token and wall ceilings by half of what they were first set to, at
  most `max_extends` times. After an extension the model is told to finalise again
  at the new ceilings. The pass ceiling is not extended. When no extension is left
  the next reached cap stops the run without asking.
- **R7. Cost is an estimate at the model's price.** The price is the built-in
  table's for the provider and model (`budget.PriceFor`), and a local model is
  free. A model with no known price leaves the cost cap off for that call, said
  once; its tokens and wall time still count.
- **R8. Wall time is time spent running turns.** The clock runs from the start of
  a turn to its end. Time between turns, and a session left open overnight, do not
  count. The wall cap is read at each model round, so a single long model call
  ends at its own end.
- **R9. A subagent spends against its parent.** An explore subagent and a `Task`
  run on the parent's meter, so their model calls add to the same totals. A
  subagent that finds a cap reached stops and never asks. A background agent is a
  separate session and is not counted against the caps of the session that
  started it.
- **R10. A repository can only narrow.** The project layer adds or lowers a cap,
  `finalize_at` and `max_extends`, never raises, clears or extends one. A user
  layer and the flags set any value. Extending is never a setting.
- **R11. A malformed cap refuses to start.** A negative or non-finite number, a
  `finalize_at` outside (0, 1), `max_extends` above 20 or a value past its range
  fails the settings load, and a session built with one does not build.
- **R12. The counters are saved and restored.** A session's totals (cost, tokens,
  passes, extensions used, wall time) are saved at the end of each turn, and when a
  cap is extended, under the state directory in `run_caps/`: a directory at 0700
  and a file at 0600 named by the session id (reduced to identifier characters,
  or its SHA-256), written atomically and never through a link. A resumed or
  reloaded session with the same id restores them and the extensions already used.
  Stop now removes the file, so a resume starts from zero. Stop and keep state
  leaves it. Files are pruned with `session_retention_days`.
- **R13. Each surface says so.** In the TUI the ask is the permission view with
  the three answers, and the highlighted one stops. Over ACP it is a
  `session/request_permission` with the same three options. A `-prompt` run cannot
  ask: it stops, prints the reply it has to stdout, a line to stderr, and exits
  with code 3. A remote session stops, and the website cannot extend it. A fleet
  worker has no meter of this kind.
- **R14. A fleet worker keeps its own budget.** The worker's per-item budget
  (`budget.*` in its profile) runs on the same meter in cancel mode. A spend past
  the budget ends the claim with the cost cause, a worker never extends, and
  `run_caps` is not read for a worker.
- **R15. The audit has one kind.** `run_cap` is an agent-scope audit event with
  the outcome `finalise`, `extended`, `stopped` or `stopped_keep`, and the data
  keys `reason` (the cap word) and `passes` (a count). It carries no text and no
  ceiling. See [audit.md](audit.md).
- **R16. Nothing a model wrote reaches a cap decision.** The meter reads usage
  numbers the provider reported and a clock. The ask and `/caps` are built from an
  enum and integers. The website's answer to the recorded ask is `deny` only.

## Edge cases

| ID | Case | Behaviour |
| --- | --- | --- |
| E1 | `finalize_at` is 0, 1 or outside the range | The settings load fails, naming `run_caps.finalize_at`. |
| E2 | A cap is negative, not a number or infinite | The settings load fails, naming the key; a flag fails the same way before any run starts. |
| E3 | A cap is small | The warning point rounds up, and a call that crosses the cap in one step is an unwarned hit, which R5 covers. |
| E4 | `max_extends` is 0 | A reached cap stops the run and asks nothing. |
| E5 | Every extension is used | The next reached cap stops the run and asks nothing. |
| E6 | A state file does not parse, has unknown keys or negative numbers | It is used for nothing and left as it is. |
| E7 | The session id has other characters, or the state directory is a link | The id is hashed into the file name and nothing is written through a link. |
| E8 | A spoken "approve", the website, the decision model or ask off answers a cap ask | None of them extends; the website's recorded options are `deny` only. |
| E9 | The pass ceiling is reached | The meter warns at `finalize_at` of `resilience.max_passes`, and the loop's own stop ends the run. It is never an ask. |
| E10 | The turn is a plan or a goal, not an agent turn | The same round boundary reads the cap, so the turn ends the same way. |

## What a run sees

A cap is checked at the top of each model round, in agent, plan and goal turns
alike, so a round already in flight finishes. The check costs a few counters and
a clock read; it makes no network call.

When a cap stops a run the turn ends with the reply so far and the stop reason
`run_cap`. A fleet worker that somehow reports it is treated as incomplete. An
approved plan's execute turn is a goal turn and is capped the same way.

## Surfaces

| Surface | Caps | The ask | Extend |
| --- | --- | --- | --- |
| TUI | settings, `-max-*` flags, `/caps` | the permission view, three answers | a key on the ask, or `/caps extend` |
| `belai -prompt` | settings, `-max-*` flags | none; the run stops, exit code 3 | not possible |
| ACP (`belai acp`) | settings | `session/request_permission`, three options | the editor's selection |
| Remote control (`belai rc`) | settings | none; the run stops | not possible |
| Fleet worker | the profile's budget | none | never |
