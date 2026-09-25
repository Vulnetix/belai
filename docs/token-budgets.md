# Token budgets

A token budget caps how many tokens one provider+model may spend in a
**session**, a local calendar **day**, or a local calendar **month**. Signet
counts every model call, shows the selected model's budgets in the footer, and
can warn when spend is outrunning the clock. Budgets never block a call: they
inform, they do not enforce.

- Manage them on the **token budgets** screen: `f1` then `b`, `/budgets`, or the
  *token budgets* row in `/settings`.
- Code: `internal/budget` (windows, colour state, the usage ledger),
  `internal/run/usage.go` (the usage observer), `internal/config/budgets.go`
  (settings), `internal/tui/budgets.go` and `budgets_view.go` (footer, warnings,
  screen), `internal/tui/components/budget_gauge.go` (the gauge).

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
  no usage, Signet estimates about four characters per token over everything
  sent and received and marks the event as estimated.
- **R5. Scope windows.** A day runs from local midnight to the next local
  midnight; a month from local midnight on the 1st to the 1st of the next
  month. A session is the Signet session: a new session (`/new`, `/clear`,
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
  `ui.budget_cycle_seconds` (default 10) before moving to the next. The cycle
  follows the clock, not a timer, so every signet window cycles in step.
- **R11. Warnings.** With `ui.budget_warn` on, each completed call to the
  selected provider+model prints one line per budget of that model that is
  amber (percentage of tokens and of time left) or red (exhausted, with usage
  and allowance). Off by default. A warning never blocks or delays a call.
- **R12. The usage ledger.** Usage persists in `usage.json` in the global
  state directory: per model per local day, and per session per model. Several
  signet processes share it through an advisory lockfile; each process sees its
  own usage at once and other processes' within 30 seconds. Day totals are kept
  for 13 months; a session's total is dropped once the session has been idle
  longer than `session_retention_days`.
- **R13. Headless runs count.** `signet -prompt` records its usage in the same
  ledger, so day and month budgets include it. It prints nothing about budgets.

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
| E10 | Routing is `routed` | Usage is recorded and the screen shows it; the footer gauge is hidden because no single model serves the turn |
| E11 | Narrow terminal | The gauge drops the time left first; then the left side (cwd and branch) is truncated, down to 20 cells so the mode chip stays; only then does the gauge drop its percentage. The scope, colour and bar always stay |
| E12 | `ui.budget_cycle_seconds` below 2, zero or negative | Below 2 is clamped to 2; zero or negative means the default, 10 |
| E13 | A second budget for the same provider, model and scope | Rejected: by settings validation, and by the screen, which points at the existing budget |
| E14 | Allowance input | Accepts whole numbers and `k`, `M`, `B` suffixes with decimals (`250k`, `1.5M`), ignoring commas, underscores and spaces; zero, negative and non-numbers are rejected |
| E15 | Two processes recording at once | Neither loses usage: every write re-reads the ledger under the lock and adds its own deltas |
| E16 | A ledger write fails | The usage stays pending in memory and is written on the next flush |
