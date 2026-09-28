# Benchmarks

A change to how Belai spends tokens ships as a default only when it has been
measured: cost per task goes down and the pass rate stays within the spread
of repeated baseline runs. This page describes the tooling and the method.
No results are published here until they have been measured this way.

- Code: `internal/run/usage.go` and `usagesummary.go` (the per-call usage
  events and their summary), the `-usage-json` flag in `cmd/belai`,
  `bench/harbor/belai_agent.py` (the Harbor adapter), `bench/arms/` (settings
  for each arm), and the `build-bench` and `bench` recipes in the justfile.

## What a run records

Every completed model call reports a usage event: provider, model, the
provider-reported prompt, completion, cache-read and cache-write tokens, and
the **role** that made the call — `agent` for the main turn and subagents,
`security` for a content classification, or the role-manager use case
(`compaction`, `mode_eval`, `web_fetch`, …). Each agent call also carries an
estimate of its request's shape: system prompt, tool definitions, history,
and tool results by tool name. These are sizes only, never content.

`belai -prompt "…" -usage-json path.json` writes the summary on exit, a
failed run included:

```json
{
  "calls": 14,
  "tokens": 182340,
  "prompt_tokens": 176020,
  "completion_tokens": 6320,
  "cache_read_tokens": 141200,
  "cache_write_tokens": 20110,
  "estimated_calls": 0,
  "by_role": { "agent": { … }, "security": { … }, "web_fetch": { … } },
  "by_model": { "anthropic/claude-sonnet-5": { … } },
  "agent_request": { "system": 21000, "tool_defs": 48000, "history": 9000,
                     "tool_results": { "Bash": 61000, "Read": 22000 } }
}
```

`agent_request` sums the estimates over agent calls. It shows where the
tokens of a run went: a large `tool_defs` points at the tool surface, and a
large `tool_results` entry at the tool whose output should be offloaded
sooner.

## Running a benchmark

The adapter runs Belai inside [Harbor](https://github.com/laude-institute/harbor)
task containers. It uploads a Linux build, runs one headless turn with
`-trust-dir -ask-permission=false` (the container is the boundary, and a
headless run cannot answer an ask), keeps guardrails on so classifier tokens
are counted, and reports the `-usage-json` summary back to Harbor for
pricing.

```sh
# Needs Docker, uv, and the provider's key in the environment
# (for example ANTHROPIC_API_KEY).
just bench terminal-bench@2.0 anthropic/claude-sonnet-5 3
just bench terminal-bench@2.0 anthropic/claude-sonnet-5 3 --ak settings=bench/arms/offload-off.json
just bench terminal-bench@2.0 anthropic/claude-sonnet-5 3 --ak settings=bench/arms/offload-1500-750.json
```

The third argument is the number of attempts per task. Other adapter options
go through `--ak`: `settings=` installs a global settings file (an arm),
`mode=` fixes the operating mode, `effort=` the thinking effort.

## Method

1. **Same model, same dataset, same attempts.** Compare arms only against one
   another, never against numbers from another harness's report.
2. **At least three attempts per task per arm.** Report the mean pass rate
   and the spread between runs. On a benchmark of about 90 tasks, one run's
   pass rate moves by several points on its own, so a difference inside
   that spread is no difference.
3. **Price every call.** Cost includes the classifier and every role-manager
   call, and uses the provider's cache prices for cache reads and writes. A
   run that failed still counts.
4. **Accept a default when** cost per task drops and the pass rate stays
   within the baseline's spread. Record the numbers, the Belai commit and
   the dataset version alongside the change.

## Arms

| File | What it tests |
| --- | --- |
| `bench/arms/offload-off.json` | Baseline: every result rides whole |
| `bench/arms/offload-4000-1500.json` | The shipped default |
| `bench/arms/offload-1500-750.json` | Aggressive: offload anything over ~6 KB |

Add an arm by writing another settings file. Anything a global settings file
can set is fair game, for example `classifier`, `routing` or `defer_tools`.
