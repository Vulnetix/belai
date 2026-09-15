# Resilient provider retry

Signet's provider path retries transient failures in two layers, mirroring the
model used by the Pi coding agent. A third layer repairs semantic failures
inside the agent loop so the model can self-correct.

```
L0  typed errors      run.ProviderError (status, retry-after, redacted body)
L1  transport retry   pre-first-byte only; rounds trips + streaming connect
L2  turn retry        agent loop re-issues the same turn
L3  semantic repair   malformed / truncated / dangling tool calls -> tool results
```

## Error classification (`internal/resilience`)

The package is intentionally leaf-only: it imports no other signet packages.
Callers supply a `Policy` and a `Classifier`; `resilience.Do` runs the attempt
loop.

Classification is fail-closed:

1. `context.Canceled` / `DeadlineExceeded` → `ClassAborted`, terminal.
2. Denylist text (`quota`, `billing`, `invalid_api_key` …) → `ClassFatal`.
3. Overflow text (`context length`, `token limit` …) → `ClassOverflow`,
   never retried; the user is told to `/compact`.
4. `StatusCoder` with 408/409/429/≥500 → `ClassRetryable`; 400/401/403/404/422
   → `ClassFatal`.
5. Retryable text (`ended without`, `connection reset` …) → `ClassRetryable`.
6. Anything else → `ClassFatal`.

Backoff honors a `Retry-After` header (seconds or HTTP-date), clamps to the
policy ceiling, then falls back to `min(0.5·2ⁿ, 8) s` with up to 25 % downward
jitter. Sleep is a first-class seam so tests never wait.

## Pre-first-byte boundary

The retry boundary is the moment before the first byte of content reaches a
consumer:

- **Blocking** (`run.go`): `client.Do` + `io.ReadAll` + status check is one
  atomic unit; failure anywhere is retryable because nothing was emitted.
- **Streaming** (`stream.go`): `client.Do` returning headers is still
  pre-first-byte. The goroutine that drains the SSE body is not.

`streamTurns` therefore separates into `openStream` (retryable) and `drainStream`
(not). Each failed `openStream` explicitly closes its body before sleeping so a
429 storm does not hold connections open.

## Turn retry and state invariants

A failed model turn has executed no tools yet, so retrying it needs no state
surgery: the same sealed `system` prompt and the same `turns` are reused. The
system prompt is sealed once per session; re-sealing would rotate nonces and
invalidate previously sealed tool-result blocks. The TUI marks a partial
assistant bubble as `Partial` so it is rendered dimly and skipped by
`buildTurns`; retries start a fresh bubble.

The retry budget is **consecutive failures**: any success resets the counter.
There is no circuit breaker or cross-session cooldown.

## Configuration

Retry budgets are configurable via `config.Settings.Resilience`:

- `max_attempts`: L2 turn retry budget per model call (default 3, same as
  the internal L1 default).
- `max_iterations`: per-prompt tool-loop budget (default 10).

Project-level `max_attempts` and `max_iterations` are constrained to the
*minimum* of the global and project values, so a cloned project file cannot
raise either budget.

## Overflow surfacing

`context length` / `token limit` failures are classified `ClassOverflow` and
are never retried. They are surfaced as a terminal error with a clear hint:
`context length exceeded; use /compact to reduce conversation size`.

## Semantic repair

Not all provider output failures should abort the turn:

- Malformed tool JSON → an `isError` tool result for that call only; siblings
  still execute. JSON parsing is deferred until execution so the accumulator
  can hand off the raw text.
- Salvageably truncated JSON (missing closing `}`/`]`/`"`) → append the
  missing delimiter and validate, but only the exact prefix case. No other
  silent truncation is allowed.
- `finish_reason: "length"` with tool calls → refuse the whole set as
  `isError` results with the message
  `"arguments may be truncated; re-issue the tool call with complete arguments"`.
- Dangling tool calls in the transcript → synthesize `"No result provided"`
  results at payload-build time only, never in stored `turns`, and without
  harness delimiters.
