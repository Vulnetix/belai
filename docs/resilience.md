# Resilient provider retry

Signet's provider path retries transient failures in two layers, mirroring the
model used by the Pi coding agent. A third layer repairs semantic failures
inside the agent loop so the model can self-correct.

```
L0  typed errors      run.ProviderError (status, retry-after, redacted body)
L1  transport retry   pre-first-byte only; rounds trips + streaming connect
L2  turn retry        agent loop re-issues the same turn
L3  semantic repair   malformed / truncated / dangling tool calls -> tool results
L4  pass boundary     goal mode: compact once on overflow, then re-run the pass
```

## Error classification (`internal/resilience`)

The package is intentionally leaf-only: it imports no other signet packages.
Callers supply a `Policy` and a `Classifier`; `resilience.Do` runs the attempt
loop.

Classification is fail-closed:

1. `context.Canceled` / `DeadlineExceeded` → `ClassAborted`, terminal.
2. Typed transport faults → `ClassRetryable` (see "Transport and HTTP/2
   failures" below).
3. Denylist text (`quota`, `billing`, `invalid_api_key` …) → `ClassFatal`.
4. Overflow text (`context length`, `token limit` …) → `ClassOverflow`,
   never retried; the user is told to `/compact`.
5. Explicit `Retry-After` header (seconds or HTTP-date) → `ClassRetryable`,
   delay clamped to the policy ceiling.
6. Rate-limit signalling without `Retry-After` (HTTP 429 or text such as
   `rate limit`, `too many requests`, `throttled`, `requests per minute`,
   the HTTP/2 `ENHANCE_YOUR_CALM` code …) → `ClassRetryable` with a
   low-pressure 60 s default backoff, also clamped to the policy ceiling.
7. `StatusCoder` with 408/409/≥500 → `ClassRetryable`; 400/401/403/404/422
   → `ClassFatal`.
8. Retryable or transport text (`ended without`, `connection reset`,
   `stream error`, `PROTOCOL_ERROR`, `GOAWAY` …) → `ClassRetryable`.
9. Anything else → `ClassFatal`.

`StatusCoder` and `RetryAfterer` are found with `errors.As`, so a
`ProviderError` wrapped by a caller keeps its status and `Retry-After`.

### Transport and HTTP/2 failures

A transport failure means no usable response came back. Retrying one on a
fresh connection is safe and usually works. Step 2 matches these types:

- `io.ErrUnexpectedEOF`
- `ECONNRESET`, `ECONNABORTED`, `ECONNREFUSED`, `EPIPE`
- any `net.Error` whose `Timeout()` is true
- `io.EOF`, but only inside a `*url.Error` (`Post "…": EOF`, meaning the server
  closed a pooled connection). A bare `io.EOF` stays fatal.

Go's bundled HTTP/2 errors aren't exported types, so step 8 matches them by
text:

- stream resets: `stream error … PROTOCOL_ERROR | INTERNAL_ERROR | REFUSED_STREAM`
- connection shutdown: `GOAWAY`, `http2: client connection lost`
- pool and connection faults: `server closed idle connection`,
  `use of closed network connection`, `connection closed before …`
- timeouts: `TLS handshake timeout`, `i/o timeout`, `stream idle timeout`
- incomplete streams: `closed without a done chunk`
- `overloaded` provider events

Edge cases:

- **Only the cause is matched.** For a `*url.Error`, steps 3, 4, 6 and 8 read
  the underlying cause and ignore the full message. The request URL is left
  out, so a host or path containing `payment`, `quota` or `token_limit` can't
  turn a connection reset into a fatal or overflow verdict.
- **Some transport errors stay fatal.** TLS certificate failures (x509) match
  none of the patterns above. A bad certificate won't fix itself.
- **Status beats text.** A response with a status code is classified by that
  status before the transport patterns run. For example, a 400 whose body says
  `INTERNAL_ERROR` is still fatal.
- **The pool is flushed before a retry.** When `client.Do` fails with no
  response, the L1 attempt calls `CloseIdleConnections` before returning. The
  retry then dials fresh instead of reusing an HTTP/2 connection the peer just
  reset or sent `GOAWAY` on. This only closes idle connections, so other
  sessions' in-flight streams aren't touched. A cancelled turn leaves the pool
  alone.
- **Mid-stream resets are retried by L2.** A reset after the first byte
  (`stream read: stream error …`) is past L1's reach, so L2 re-issues the whole
  turn, and the TUI dims the partial bubble.

Backoff honors an explicit `Retry-After` header first, then the rate-limit
default when rate limiting is detected but no header is supplied, and finally
falls back to `min(0.5·2ⁿ, 8) s` with up to 25 % downward jitter. Sleep is a
first-class seam so tests never wait.

### Rate-limit edge cases

- A `Retry-After` value of `0` is treated as absent: Signet falls back to the
  rate-limit default or exponential backoff rather than retrying immediately.
- A missing `Retry-After` header on a 429 does **not** mean "retry instantly";
  the 60 s default gives per-minute inference limits time to clear. If that is
  longer than a configured ceiling, the ceiling wins.
- Denylist text such as `insufficient_quota` overrides the 429 or rate-limit
  text and remains fatal, because retrying a billing or key problem only burns
  budget.
- Overflow text is checked before rate-limit text, so a `context length
  exceeded` response is never mistaken for a rate limit.

### Policy defaults

`Policy` is a plain struct, so a caller may read its fields directly instead of
going through `resilience.Do`. `Do` and `Delay` normalise internally, but a
bare `Policy{}` still has a nil `Rand` and a nil `Sleep` — calling either
panics. `Policy.WithDefaults()` returns a normalised copy and is what any
caller reading those fields (L2 turn retry) must use.

| Field | Zero value becomes | Note |
| ----- | ------------------ | ---- |
| `MaxAttempts` | 3 | inclusive of the first attempt |
| `Base` | 500 ms | |
| `Cap` | 8 s | caps the exponential term |
| `Ceiling` | 60 s | absolute cap; also bounds `Retry-After` |
| `Jitter` | **stays 0** | the one field with no non-zero default: 0 means no jitter. Signet's L1 and L2 policies both opt into 0.25 |
| `Rand` | `rand.Float64` | |
| `Sleep` | context-aware `time.After` | |

Jitter is opt-in rather than defaulted because zero is a legitimate explicit
choice (deterministic backoff in tests) and a Go zero value cannot distinguish
"unset" from "none". Both real policies set it, so every retry Signet issues in
production is jittered.

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
system prompt is sealed once and reused while its inputs are unchanged;
re-sealing would rotate nonces and
invalidate previously sealed tool-result blocks. The TUI marks a partial
assistant bubble as `Partial` so it is rendered dimly and skipped by
`buildTurns`; retries start a fresh bubble.

The retry budget is **consecutive failures**: any success resets the counter.
There is no circuit breaker or cross-session cooldown.

L2 takes its delay from the classifier's verdict. A rate-limit error with no
`Retry-After` therefore waits the same 60 s default at L2 as it does at L1,
rather than falling back to a 0.5 s exponential retry. An explicit
`Retry-After` on a (possibly wrapped) `ProviderError` still takes precedence.

Each retry event includes the budget of the layer that is retrying
(`resilience.Attempt.Max` → `agent.Event.RetryMax`). The TUI shows it as
`retrying (n/max)`, or as `retrying (n)` when the budget is unknown. L1 retries
count against the fixed transport budget of 3. L2 retries count against
`max_attempts`.

## Configuration

Retry budgets are configurable via `config.Settings.Resilience`:

- `max_attempts`: L2 turn retry budget per model call (default 3, same as
  the internal L1 default).
- `max_iterations`: per-pass tool-loop budget (default 40).
- `max_passes`: goal-mode pass-loop ceiling (default 0 — unbounded). The pass
  loop's own stall detectors are what normally stop it; this exists for CI and
  for anyone who wants a hard bound on spend. When it is reached the loop
  returns `goal pass loop stopped: max passes (N) reached`.
- `max_clarify_rounds`: bounds the explore→clarify→explore loop (default 3).
  A **negative** value is the documented way to disable clarification
  entirely: the accessor passes the sign through unclamped and
  `clarifyRounds` returns immediately on it. Zero is not a disable — zero
  means "use the default", like every other budget here.
- `max_explore_iterations`: the tool-loop budget of a single explore subagent
  (default 8, raised from a historical 4 so a subagent actually runs
  `rg`/`find`/`git` before clarifying, while keeping the fan-out bounded).

Zero always means "unset, use the default" — which is why `max_passes` needs
its own rule below, and why a budget genuinely cannot be set to zero.

Project-level values are constrained to the *minimum* of the global and project
values, so a cloned project file cannot raise a budget. Every budget follows
that rule, including `max_clarify_rounds` and `max_explore_iterations`. For
`max_passes` an unset global (0, unbounded) takes the project value: there is
no ceiling to lower, and adding one is a tightening, not a relaxation. The
same "unset global takes the project value" step applies to the others, where
it is a relaxation only against a default the global file never stated.

## Overflow surfacing

`context length` / `token limit` failures are classified `ClassOverflow` and
are never retried at L1 or L2. They are surfaced as a terminal error with a
clear hint: `context length exceeded; use /compact to reduce conversation size`.

## Pass-boundary recovery (goal mode)

The goal pass loop adds the one place an overflow is recoverable. An overflow
escaping a pass is caught **once** per prompt: the loop compacts at the pass
boundary and re-runs the pass. A second overflow is terminal, and every other
error is terminal immediately — L2 already owns the retry budget, so retrying
again here would multiply it.

Compaction runs only at a pass boundary. Mid-pass, `turns` may hold an
assistant turn carrying `tool_calls` whose matching tool turns are not yet
appended; truncating there orphans `tool_call_id`s and providers reject the
payload. The trigger is an estimated context above 70 % of the resolved model
window. The window resolves as user override → provider catalogue
(`Settings.CatalogWindow`) → built-in `modelinfo` registry →
`defaultCompactWindow` (128k). That last step is load-bearing: the registry
alone answers "unknown" for any model that exists only in a custom provider
catalogue, and an unknown window used to mean *never compact*, so a long goal
run against such a model grew until a request overflowed. Guessing low only
ever compacts sooner, so compaction guesses; the TUI still reports an unknown
window rather than a guessed denominator.

The summary re-enters through Role Manager admission (see
[role-manager.md](role-manager.md), "Compaction at the boundary"), and any
failure — no window, no summary, refused admission — simply skips compaction
for that boundary.

A **broken goal evaluator** is recovered rather than fatal. A reply that is
not a bare sentinel is re-asked once, quoting the rejected text and the three
accepted tokens back, before it counts as malformed; models break the
single-token contract in repairable ways (a leading `Answer:`, a code fence, a
sentence of reasoning). Two malformed verdicts in a row end the loop, but they
return the work so far with `GOAL_PARTIAL` and a warning — never an error. The
passes that ran changed real files, and a garbled classifier token is not a
reason to discard them. An evaluator *transport* failure stays terminal: the
verdict is unknown, and an unknown verdict must not grant compute. The plan
pass loop keeps the stricter contract — no repair round, and two malformed
`PLAN_*` replies are an error. The terminal error still records the plan
artifact before it surfaces (see role-manager.md, "Plan file"), so the
stricter evaluator contract costs no plan text.

Cancellation is the loop's only true ceiling, and it is not an error: `esc` in
the TUI or `SIGINT` on the CLI returns the partial result wrapped in
`ErrPassLoopCancelled`, never a raw `context.Canceled` the transcript would
print as an agent failure. Non-TUI entry points install a
`signal.NotifyContext` root for exactly this reason; a second signal hard-exits
with status 130, because the next pass boundary may be seconds away.

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
