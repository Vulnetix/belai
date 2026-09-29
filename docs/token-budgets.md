# Token budgets

A token budget caps how many tokens one provider+model may spend in a
**session**, a local calendar **day**, or a local calendar **month**. Belai
counts every model call, shows the selected model's budgets in the footer, and
can warn when spend is outrunning the clock. Budgets never block a call: they
inform, they do not enforce.

The same ledger feeds **session intelligence**: plan limits with their reset
times, pace, trend, runway, and today, week and 30-day roll-ups. It has its own
slot in the footer cycle (shown even under `routed` routing and with no budget
set), and `f12` opens a pane with an interactive dashboard. See
[Session intelligence](#session-intelligence).

- Manage budgets on the **token budgets** screen: `f1` then `b`, `/budgets`, or
  the *token budgets* row in `/settings`.
- Open session intelligence with `f12` (the runs panel's **intel** tab), `/intel`,
  or `f1` then `i`.
- Code: `internal/budget` (windows, colour state, the usage ledger,
  `intel.go` for roll-ups, pace, trend and runway, `planlimit.go` for plan
  limits), `internal/run/usage.go` (the usage observer) and
  `internal/run/planlimits.go` (the response-header parser),
  `internal/config/budgets.go` (settings), `internal/tui/budgets.go` and
  `budgets_view.go` (footer cycle, warnings, screen), `internal/tui/intel_panel.go`
  (the pane and the full screen), `internal/tui/components/budget_gauge.go` (both
  gauges) and `sparkline.go` (sparkline and limit bar).

Every rule (**R**) and edge case (**E**) below is pinned by a unit test named
after it — `TestBudgetRule7_…`, `TestBudgetEdge3_…` — and
`TestBudgetDocParity` fails when an ID here has no test or a test names an ID
that is not here.

## Settings

| Key | Layer | Default | Meaning |
| --- | --- | --- | --- |
| `token_budgets` | global only | none | List of `{provider, model, scope, tokens}` |
| `ui.budget_cycle_seconds` | any | `10` | Seconds the footer shows each budget before cycling (minimum 2) |
| `ui.budget_warn` | any | `false` | Print a warning line on each call while a budget is amber or red |
| `ui.intel` | any | `true` | Session intelligence: the footer slot, the intel tab and `f12`, `/intel` |
| `intel.plan_limits` | global only | `true` | Read provider rate-limit response headers into plan-limit readings |

```json
{
  "token_budgets": [
    { "provider": "openrouter", "model": "anthropic/claude-sonnet-5", "scope": "session", "tokens": 2000000 },
    { "provider": "openrouter", "model": "anthropic/claude-sonnet-5", "scope": "day", "tokens": 20000000 }
  ],
  "ui": { "budget_cycle_seconds": 10, "budget_warn": true }
}
```

## Business rules

- **R1. One budget per provider, model and scope.** A budget is keyed by
  provider, model and scope (`session`, `day` or `month`), so a model has at
  most three. Provider and model are required, the scope must be one of the
  three, and the allowance must be a positive whole number of tokens. An
  invalid budget fails settings resolution, like an invalid provider profile.
- **R2. Budgets are global.** They live only in the global settings file. A
  `token_budgets` key in a project's `.vulnetix/settings.json` is ignored with a
  note — a repository must not raise or remove the limits a user set for their
  own spend. The budgets screen always writes the global file, whatever scope
  `/settings` is on.
- **R3. Every completed call counts, under the model that served it.** The main
  turn (streaming or not), subagents, background agents, and every
  role-manager and classifier call (mode selection, goal and plan evaluation,
  compaction, clarify, session naming, the security classifier) report their
  usage through one observer in `internal/run`. Usage is recorded under the
  provider and model that actually served the call, so a fast-tier or routed
  role call counts against that model's budgets, not the selected model's.
- **R4. Tokens are the provider's total.** A call counts its provider-reported
  total: prompt plus completion, reasoning included. When a provider reports
  no usage, Belai estimates about four characters per token over everything
  sent and received and marks the event as estimated.
- **R5. Scope windows.** A day runs from local midnight to the next local
  midnight; a month from local midnight on the 1st to the 1st of the next
  month. A session is the Belai session: a new session (`/new`, `/clear`,
  plan execution) starts from zero, and a resumed session picks its stored
  total back up.
- **R6. Token percentage left rounds up.** The percentage of the allowance
  left is rounded up, so the footer shows 0% only when the budget is
  exhausted, never while tokens remain.
- **R7. Red when exhausted.** A budget is red once tokens used reach the
  allowance. Red outranks amber, and amber outranks teal.
- **R8. Amber when spend outruns the clock.** A day or month budget is amber
  when a larger share of its time window is left than of its tokens — at the
  current pace it runs out before the window ends. The comparison uses exact
  fractions, so display rounding never changes the colour. A session budget has
  no window: it shows no time and is never amber.
- **R9. The footer gauge.** The selected provider+model's budget is shown
  right-aligned on the footer's first line only when routing is `defined` and
  that model has at least one budget: scope, token percentage left, time left
  (day and month only), and a bar whose used share is filled in the state
  colour — teal, amber or red — over a grey trough. The scope, percentage and
  time take the same colour.
- **R10. Cycling.** With two or more budgets for the selected model the footer
  shows each in scope order (session, day, month) for
  `ui.budget_cycle_seconds` (default 10) before moving to the next; the
  session intelligence slot is the last stop of the cycle (R15). The cycle
  follows the clock, not a timer, so every belai window cycles in step.
- **R11. Warnings.** With `ui.budget_warn` on, each completed call to the
  selected provider+model prints one line per budget of that model that is
  amber (percentage of tokens and of time left) or red (exhausted, with usage
  and allowance). Off by default. A warning never blocks or delays a call.
- **R12. The usage ledger.** Usage persists in `usage.json` in the global
  state directory: per model per local day, per model per local hour (tokens and
  calls, kept eight days), the latest plan-limit readings, and per session per
  model. Several
  belai processes share it through an advisory lockfile; each process sees its
  own usage at once and other processes' within 30 seconds. Day totals are kept
  for 13 months; a session's total is dropped once the session has been idle
  longer than `session_retention_days`, and it is then remembered as
  imported so its transcript is never counted a second time.
- **R13. Headless runs count.** `belai -prompt` records its usage in the same
  ledger, so day and month budgets include it. It prints nothing about budgets.
- **R14. Day and month cover every session.** Day and month budgets are
  global: they count every session of every project, not only the one on
  screen. Sessions the ledger never recorded live — saved before token budgets
  existed, or by an older belai — are imported from their transcripts under
  `sessions/` when the TUI starts, in the background. A goal run counts its
  exact `tokensUsed`, split across days by its `goal_state` rows and recorded
  under the model its turns name; every other turn counts the usage its
  transcript row kept (its last model call), so that part is a lower bound, and
  classifier and role-manager calls outside a goal are not recoverable.

## Edge cases

| ID | Case | Behaviour |
| --- | --- | --- |
| E1 | Usage exactly equals the allowance | 0% left, red, bar full |
| E2 | Usage past the allowance | Clamped to 0% left and a full bar; red |
| E3 | A daylight-saving day | The day window is 23 or 25 hours; time left and its percentage use the real length |
| E4 | Month rollover (and 29 February) | Only the current local month's days count; the previous month's spend drops out at local midnight on the 1st |
| E5 | Provider reports no usage | Estimated at ~4 characters per token over request and reply; flagged as estimated |
| E6 | A call fails or is cancelled | Nothing is recorded: providers report usage only with a completed response |
| E7 | A lockfile left by a crashed process | Taken over once it is older than 30 seconds |
| E8 | `usage.json` does not parse | Moved to `usage.json.corrupt-<unix>`, counting restarts from zero, and the TUI says so |
| E9 | A budget for a model that is not selected | Listed on the budgets screen and keeps counting (e.g. when role routing uses that model); never in the footer |
| E10 | Routing is `routed` | Usage is recorded and the screen shows it; the footer budget gauge is hidden because no single model serves the turn, and the session intelligence slot holds the line instead (R15) |
| E11 | Narrow terminal | The gauge drops the time left first; then the left side (cwd and branch) is truncated, down to 20 cells so the mode chip stays; only then does the gauge drop its percentage. The scope, colour and bar always stay |
| E12 | `ui.budget_cycle_seconds` below 2, zero or negative | Below 2 is clamped to 2; zero or negative means the default, 10 |
| E13 | A second budget for the same provider, model and scope | Rejected: by settings validation, and by the screen, which points at the existing budget |
| E14 | Allowance input | Accepts whole numbers and `k`, `M`, `B` suffixes with decimals (`250k`, `1.5M`), ignoring commas, underscores and spaces; zero, negative and non-numbers are rejected |
| E15 | Two processes recording at once | Neither loses usage: every write re-reads the ledger under the lock and adds its own deltas |
| E16 | A ledger write fails | The usage stays pending in memory and is written on the next flush |
| E17 | Two processes import history at once | Each session is counted once: the import re-reads the ledger under the lock and skips any session already imported |
| E18 | A session that was recorded live | Never imported from its transcript — not while its ledger entry is kept, and not after an idle entry is pruned (it moves to the imported set); a TUI never imports its own session |

## Session intelligence

Session intelligence reads the same ledger and answers a different question:
how fast is spend going, and will the allowance last. It shows plan limits with
their reset times, pace, trend, runway, and today, week and 30-day roll-ups. It
draws harness facts only (token counts, hour buckets, provider and model
identifiers, times, and numbers parsed from response headers), and nothing on it
reaches a model turn, a system block, a directive or telemetry.

Settings are in the table above. `ui.intel` may be set by any layer;
`intel.plan_limits` is read from the user's own layers only.

### Session intelligence rules

- **R15. The cycle ends with an intel slot.** With `ui.intel` on (the default),
  the footer's right side cycles through the selected model's budgets in scope
  order and then one session intelligence slot, each for
  `ui.budget_cycle_seconds`. With no budget for the model, or with routing
  `routed`, the intel slot is the only slot, so usage is visible whatever the
  routing and whether or not a budget exists. A budget and the intel slot never
  draw together.
- **R16. The intel slot.** It is right-aligned on line 1 like the budget gauge:
  the word `intel`, today's tokens, the tightest plan limit as a label and the
  percentage used (or the pace label when the provider reports no limit), and a
  10-cell picture. It has the gauge's three detail levels: everything; without
  the limit or pace; the label and the picture only. The picture is the
  tightest limit's used share with a `╹` marker at the share of its window that
  has passed (fill beyond the marker is ahead of the clock), else a 24-hour
  sparkline, else a dotted trough when nothing has been spent. Tightest means
  five-hour, then seven-day, then the per-minute tokens, input, output and
  requests allowances. The slot is red when a plan window, or the day or month
  budget standing in for one, is spent; amber when at the current pace the
  allowance runs out before its window ends (R20); and teal otherwise.
- **R17. The shortcut hint.** In the last third of each cycle period the intel
  slot appends `f12` in the lowest-contrast colour. The hint is drawn only when
  the whole line still fits with it; when it would not fit it is dropped, and
  nothing else is shed to make room. It follows the clock like the cycle, so
  every belai window agrees, and it never appears on a budget.
- **R18. Windows and sources.** The windows are today (from local midnight), the
  last 7 local days and the last 30, each including today and ending now. Token
  totals and the trend read day totals, which include imported transcripts.
  Calls, pace, the 24-hour sparkline and the 7-day heatmap read hour buckets:
  tokens and completed calls per model per local hour, kept eight days, so the
  30-day window's call count covers those eight days. A window counts a session
  by its last activity: a live session by its last update, an imported one by
  the day its transcript was last active, and this process's own session once it
  has spent a token. The request composition on the full screen sums the
  estimated size of what this session's agent calls sent (system prompt, tool
  definitions, history, tool results by tool), never content.
- **R19. Plan limits are parsed numbers.** One parser reads the response
  headers of every completed call. It understands Anthropic's unified plan
  windows (`anthropic-ratelimit-unified-5h-*` and `-7d-*`: utilisation and a
  reset in epoch seconds or RFC 3339), Anthropic's per-key allowances
  (`anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-*`),
  the OpenAI dialect's `x-ratelimit-*` (reset as a duration such as `6m0s`), and
  OpenRouter's bare `x-ratelimit-*` (reset in epoch milliseconds). A reading
  keeps a window, a used share, the limit and remaining where reported, and a
  reset time. It is kept only when every value is a number of the expected shape,
  the used share is 0 to 1, remaining does not exceed the limit, and the reset is
  from an hour ago to eight days ahead; anything else is dropped and no header
  text is stored. Of two readings for one provider and window the later
  observation wins. Readings are per provider (under `routed`, the provider of
  the selected model), are written to `usage.json`, and a reading whose window
  has reset is hidden (E24).
- **R20. Pace, trend and runway.** Pace is the tokens of the trailing 60
  minutes: the elapsed part of this hour plus the matching share of the
  previous one; it is `idle` at zero. Trend is today's tokens against the mean of
  the previous seven days, scaled to the share of today that has passed (at
  least one hour of it): under 0.8 is `easing`, over 1.25 is `rising`, between is
  `steady`, and `new` when the previous seven days hold nothing. Runway projects
  the tightest allowance at the current pace. Against a plan window it is
  calibrated from this ledger: the share of the window spent per token recorded
  since the window opened, times the pace, gives the percentage of the window
  spent per hour and the time to run out; the allowance lasts when that time is
  at least the time to the reset. Without a calibrated plan window the selected
  model's day and month budgets stand in (a session budget has no window), and
  with neither the runway is `no limit set` and a non-zero pace is `active`. The
  pace label is `comfortable` when the allowance lasts, `brisk` when it runs out
  at least a quarter of the way to the reset, and `hot` when sooner.
- **R21. The pane and the full screen.** `f12` toggles the runs panel on its
  **intel** tab, focused; with the panel open on another tab it switches to
  intel. `tab` reaches the tab, and the tab takes half the terminal height
  instead of a third. The pane shows the selected model, today's tokens, calls
  and sessions and the age of the limit reading; one row per plan limit with its
  bar, reset marker and time to reset (or a line saying the provider reports
  none); pace and trend; the runway; then today as a 24-hour sparkline, and the
  week and the 30 days as shares of the 30-day total. Keys: `left` and `right`
  move the window (today, this week, last 30 days), `m` lists the models of the
  window, `r` lists this session's usage by role, `t` returns to the timeline,
  `b` opens the budgets screen, `enter` opens the full screen, `esc` unfocuses
  and `f12` or `f9` closes. The full screen (also `/intel` and `f1` then `i`)
  adds the day and month budgets beside the limits, a 7-day by 24-hour heatmap
  (four shades, quantised against the busiest hour), a selectable models or
  roles table, the request composition, and `R` to re-read the ledger.
- **R22. Settings.** `ui.intel` (any layer, default on) turns the footer slot,
  the intel tab, `f12` and `/intel` off; budgets then keep the cycle to
  themselves. `intel.plan_limits` (global only, default on) turns the header
  parsing off. A project layer's `intel` key is dropped with a note: what a user
  learns about their own account limits is theirs to decide, not a repository's.

### Session intelligence edge cases

| ID | Case | Behaviour |
| --- | --- | --- |
| E19 | A version 1 `usage.json` | Loads unchanged with no hour buckets and no limits; the first write upgrades it to version 2 and keeps every day, session and import marker |
| E20 | An older belai writes the ledger | It drops the hour buckets and limits it does not know; the intel views are empty until the next calls refill them, and day totals and roll-ups are untouched |
| E21 | A provider sends no limit headers (Copilot, Kiro, Workers AI, the AI Gateway), or a proxy strips them | No limit rows; the pane says `no plan limit reported by <provider>`, and the footer picture is the 24-hour sparkline |
| E22 | The roles list | It is this process's tally, kept in memory (the ledger has no role), so it speaks for this session and a second process starts with none |
| E23 | An imported transcript | It has days only: it counts in the roll-ups, the trend and the session counts, never in the sparkline, the pace or the call counts, and its heatmap row is drawn flat and marked `day total` |
| E24 | A plan window has reset since its last reading | The reading is hidden until a fresher one arrives; no runway is projected from it |
| E25 | A narrow or short terminal | The footer sheds the slot's detail first, then the left side to 20 cells, and keeps the label and the picture; the hint is dropped when tight. The pane drops header lines from the bottom, keeping the tab bar, the help line and at least one list row |
| E26 | `ui.intel` is off | The slot, the tab, `f12` and `/intel` are gone (`/intel` says so); budgets alone hold the cycle |
| E27 | Retention | Hour buckets and limit readings are pruned after eight days; day totals after 13 months |
