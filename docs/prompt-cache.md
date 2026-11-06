# Prompt cache

**Status:** new.

A provider that caches a request prefix charges a fraction of the price for the
part it has seen before, and forgets the prefix after a short time. Belai
already marks the cache breakpoints on a native Anthropic request. This page
covers three additions: a longer lifetime, an opt-in ping that keeps an idle
session's prefix warm, and a notice when the prefix changes by itself.

All three are the user's. A longer write and every ping spend the user's
tokens, so the `prompt_cache` block is read from your own settings layers only.

- `prompt_cache.ttl` asks for a `1h` lifetime instead of the default `5m`.
- `prompt_cache.keepalive_max` sets how many pings an idle stretch may send. It
  is `0`, off, by default.
- `/cache` shows the cache and changes both settings.
- A `cache_prefix_changed` row appears in the activity feed when the stable
  system block or the tool definitions change between two calls of one
  session.
- Code: `internal/config/promptcache.go` (settings), `internal/wire/wire.go`
  (the `ttl` field), `internal/run/run.go` (breakpoints),
  `internal/run/keepalive.go` (the ping and the prefix check),
  `internal/tui/cache_cmd.go` (`/cache`), `internal/rolemanager/promptcache.go`
  (the activity row).

## Settings

The `prompt_cache` block has no row on a settings screen. Set it in the settings
file or with `/cache`.

| Key | Meaning | Default |
| --- | --- | --- |
| `prompt_cache.ttl` | Cache lifetime on every breakpoint: `5m` or `1h`. | `5m` |
| `prompt_cache.keepalive_max` | Most pings for one idle stretch, 0 to 100. `0` is off. | `0` |
| `prompt_cache.keepalive_every_s` | Idle seconds before a ping, 60 to 3500. | 270 with `5m`, 3300 with `1h` |

A `ttl` that is not `5m` or `1h`, or a number outside its range, fails the
settings load and names the key. A gap that is valid but too long for the
lifetime is clamped, not refused: at most 290 seconds with `5m` and 3500 with
`1h`, so a ping always lands before the entry it refreshes has expired.

A project file cannot set the block. Its value is ignored with a note, and the
origin of `prompt_cache` is never the project layer.

## The longer lifetime

With `ttl` set to `1h`, the four breakpoints of an Anthropic request (the
stable system block, the last system block, the last tool definition and the
newest message) all carry `"ttl": "1h"` in their `cache_control`. They carry
the same value so a longer entry never follows a shorter one. With `5m`
nothing is added, and the request is byte for byte what it was before.

A one hour write costs more than a five minute one, and a read costs the same.
It pays when a session is idle for more than five minutes between turns. The
field is sent only where the harness already sends breakpoints, which is the
native Anthropic provider. A gateway route, a custom Anthropic-shaped server,
Bedrock and Kiro keep their own lifetime. The field is built from the provider's
documented request shape and has not been run against the live API in this
repository's tests; if a provider refuses it, `/cache ttl 5m` goes back.

## The keep-alive

With `keepalive_max` above zero, the interactive session remembers the last
main-turn request. When the session has been idle for `keepalive_every_s`, the
harness (never the model) sends that request again as a ping, which refreshes
the provider's entry.

A ping:

- goes through the request factory a turn uses, so it takes the same provider,
  credential, firewall and network checks, with no second sender;
- is one blocking call whose `max_tokens` is 1, or one above the thinking
  budget when the model thinks, because the API needs the budget below the
  cap. The system blocks, tools, messages and thinking fields are unchanged, so
  the call reads the cached prefix;
- discards the reply. Nothing from it enters the conversation, the transcript,
  a classifier or a hook, and no tool runs. A ping is not a turn, so no hook
  fires for it;
- is booked like any model call. Its tokens reach the budget ledger under the role `keepalive`, count against token
  budgets, and are left out of the agent-only figures (cache share, reply cost).
  With a thinking budget a ping can still generate up to that budget.

Pings stop for good, and the stored request is dropped, when:

- the count is reached (a new turn starts a new idle stretch with a fresh count);
- a ping gets any answer but a 2xx, a 429 included, or fails before it is sent.
  It is not retried, and pings stay off for the session until
  `/cache keepalive N` turns them on again.

Pings are held, without being dropped, while:

- a turn is running or a prompt is being admitted, so a user prompt always
  goes first;
- plan mode is on, or a permission, clarification or plan-review prompt is open;
- a token budget for the selected model is exhausted;
- the provider is a local one.

The stored request is dropped, so nothing is replayed, when the session is
rebuilt (a model, mode, tool set or settings change), when `keepalive_max` is
set to `0`, and when the session ends. It is held in memory only.

Only the interactive terminal session pings. A headless run, an ACP session, a
subagent, a fleet worker and a remote-control session never do: they are built
without a keep-alive. They still use the `ttl` setting.

## The prefix check

Each main-turn call hashes the stable system block (nonce removed, as the
OpenAI cache key is) and the tool definitions. When the hash changes between
two calls of one session that have the same tool names, the harness records a
`cache_prefix_changed` activity with the part that changed, `system` or
`tools`. A different tool set is expected (a mode switch, a deferred tool
loaded) and only moves the baseline. The row holds the part name and nothing
else: no prompt text and no tool name. It shows at the `decisions` level of the
activity feed.

Editing an instruction file mid-session changes the stable block, so the row
also appears then. It means the provider will write the prefix again on the next
call.

## `/cache`

| Command | Effect |
| --- | --- |
| `/cache` or `/cache status` | The `ttl`, the last call's and the session's read share, tokens written, the pings sent of the count, the time to the next one and what the pings have spent. |
| `/cache ttl 5m` or `/cache ttl 1h` | Writes `prompt_cache.ttl` to your global settings file and rebuilds the session so the next request uses it. |
| `/cache keepalive N` or `/cache keepalive off` | Writes `prompt_cache.keepalive_max` (0 to 100) to your global settings file and clears a stopped state. |

The command is the terminal's. A web session cannot send it. Both writes go to
the user file, so a project file can neither choose nor override them.

## Edge cases

- The ping count is per idle stretch, not per session: each real model call
  resets it.
- Changing the model, mode or tool set rebuilds the session and drops the stored
  request, so no ping replays a prefix for a model the user left. Pings resume
  after the next real call.
- `keepalive_max` set to `0` stops the loop and drops the stored request.

## Tests

`internal/config/promptcache_test.go` pins the defaults, the clamps, the
validation and the project-layer rule. `internal/run/keepalive_test.go` pins the
ttl on every breakpoint, the ping's shape, each stop condition on a fake clock,
that only a main turn is remembered, and the prefix check.
`internal/tui/cache_cmd_test.go` covers `/cache` and the holds.
