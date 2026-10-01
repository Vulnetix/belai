# Context offload

Most of what an agent sends a model is tool output: build logs, test runs,
web pages, pull-request threads. Belai keeps that output from filling the
context window in four ways, all of which keep the conversation prefix
byte-stable so the provider's prompt cache holds.

- Code: `internal/offload` (store, preview, slicing),
  `internal/tools/readresult.go` (the `ReadResult` tool),
  `Session.promoteResult` and `offloadAdmitted` in `internal/agent/agent.go`,
  `internal/rolemanager/webfetch.go` (the `web_fetch` role),
  `internal/proc/linetee.go` (Bash head and tail).
- Settings: `offload` (below).

## Offloading oversized results

When a tool result is larger than `offload.threshold_tokens` (estimated at
four characters per token), the conversation gets a preview in its place:

```
<first lines of the output>
[… 2811 lines elided …]
<last lines of the output>
[Offloaded Bash output: ~31000 tokens, 3001 lines. Only the head and tail are shown. Read the rest with ReadResult(ref="r3") and offset/limit (1-based lines) or pattern (RE2).]
```

- The preview takes `offload.preview_tokens`, 60% from the head and 40% from
  the tail, cut on whole lines. A result that is one very long line is cut
  inside the line and the marker counts characters.
- The tail matters: a build or test run prints its summary last.
- The preview is written into the tool turn once, when the result is added.
  Later requests carry the same bytes, so the cached prefix is unchanged.
- The kinds offloaded are the ones whose size Belai does not shape: `Bash`,
  `WebFetch`, `WebSearch`, `GH`/`Glab` and other `KindRemote` results, MCP
  tools, `SubAgentLog`, `BashOutput` and `Task` reports. `Read` is never offloaded: it
  already pages with `offset`/`limit`, and `Edit` needs the exact bytes.

### Reading the rest

`ReadResult(ref, pattern?, context?, offset?, limit?)` returns part of an
offloaded result, numbered like `Read`:

- with `pattern` (RE2), the matching lines with `context` lines around each
  (default 3);
- otherwise `limit` lines (default 200) from the 1-based `offset`.

`ReadResult` reads what the tool returned, not more: Bash still captures at
most 64 KiB (its first and last 32 KiB, below), because the whole capture is
classified before it can be offloaded, and a larger capture would cost
classifier tokens on every big command. What offload saves is the context:
the preview replaces up to 64 KiB on this and every later request.

A slice is bounded at 16 KiB and each line at 2,000 characters. `ReadResult`
is a core tool, so the model can call it without `ToolSearch`. It is only
registered while offload is on.

### Security

- Only admitted content is stored. Offload runs in `promoteResult` after the
  result was sanitised and classified (or after sanitising with guardrails
  off). A withheld result is never stored.
- `ReadResult` is path-free. It takes a reference such as `r3`, which
  resolves only in its own session's store. It cannot name a file.
- Its slices are `KindOffload`, which is in `tools.classifierKinds`: a slice
  is the same arbitrary bytes read back, so it is classified again rather than
  trusted on the earlier verdict.
- The store is in memory only, capped at 64 MiB per session (8 MiB per
  result, keeping the end of a longer one), oldest first out. Nothing is
  written to disk. A reference that was evicted, or that a resumed session
  never had, answers that it is not available.

## WebFetch answers the question

`WebFetch` takes an optional `prompt`, the argument shape models know from
other harnesses. With a prompt:

1. the page is fetched and stripped of markup as before;
2. the fast-tier `web_fetch` role gets the sanitised page text (up to 50,000
   characters) and the prompt, with no tools, skills or agent block, and is
   told the page is untrusted data;
3. its answer, not the page, becomes the `WebFetch` result and goes through
   the classifier;
4. if the role fails or answers nothing, the page itself is used, and a long
   page is offloaded as above.

The role routes like the other role-manager activities: set a model for
`web_fetch` under `routing.use_cases`, or let it default to the fast tier.

## Bash keeps the head and the tail

Bash output is capped at 64 KiB. The cap keeps the first and the last 32 KiB
with a line saying how many bytes were elided, instead of only the first
64 KiB, so a failing test's summary is never the part that is cut.

## Overflow recovery

When a provider rejects a request as larger than its context window, the pass
loop recovers once: it compacts whatever the size estimate said (the estimate
evidently undercounted), and if compaction fails it clears every tool result
except the two newest. Only then does the error end the turn.

## Settings

| Key | Default | Meaning |
| --- | --- | --- |
| `offload.enabled` | `true` | Offload oversized results and register `ReadResult` |
| `offload.threshold_tokens` | `4000` | Estimated size above which a result is offloaded |
| `offload.preview_tokens` | `1500` | Estimated size of the inline preview (at least 200) |

Two rules keep a bad value from doing harm. A threshold or preview of zero or
less is treated as unset and takes its default. After that the preview is raised
to 200 if it is lower, and a threshold that is not above the preview is raised
to twice the preview, so a result is never replaced by a preview as large as the
result itself.

Offload changes how much admitted content rides on each request, never what is
admitted, so any settings layer may set these. The defaults are a starting
point; [benchmarks](benchmarks.md) describes how to measure other values.
