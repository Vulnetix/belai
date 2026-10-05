# Code mode

**Status:** alpha. New in this release.

Code mode is agent mode with one extra tool, `Code`. The model writes a short
JavaScript script that calls tools as functions, and only what the script
prints comes back into the conversation. A job that would take thirty tool calls
(read forty files, count matches, edit five places, ask an MCP server for the
related tickets) becomes one call, and the intermediate results never enter
the context.

The idea comes from Pi's code mode (the `pi-codemode` and `pi-code-tool`
extensions), Cloudflare's Code Mode and Anthropic's "code execution with MCP".
Belai's version keeps the gates it has everywhere else: a script has no more
authority than the same calls made one by one.

- [Choosing it](#choosing-it)
- [What the model sees](#what-the-model-sees)
- [The script environment](#the-script-environment)
- [MCP in code mode](#mcp-in-code-mode)
- [Security model](#security-model)
- [Settings](#settings)
- [Limitations](#limitations)

## Choosing it

Code mode is an explicit choice. It is never picked by the classifier and is not
in the mode-choice panel or the mode key's cycle.

| Where | How |
| --- | --- |
| TUI | `/mode code` (sticky, saved for the project like any mode) |
| `belai -prompt` | `-mode code` |
| ACP | the editor's mode picker offers **Code** |
| `belai rc` / web controls | `mode: code`, and `/mode code` from the web |
| Project preference | `mode` = `code` |

`/mode agent` (or `plan`, `goal`, `auto`) leaves it. Plan mode, goal mode and a
named agent profile never get the `Code` tool.

## What the model sees

The request carries the agent-mode surface with two differences:

- `Code` is added, and it is a core tool, so its definition is always sent.
- MCP tools are not advertised and cannot be called directly. A direct call is
  withheld with a message that names the script form.

The system text gains a short "Code mode" section saying when to use a script
and when not to. Single reads and edits stay native calls, because a trained
tool schema is obeyed more reliably than a new one.

## The script environment

A script is a function body. It may `return` a value, which is printed. The
interpreter is [goja](https://github.com/dop251/goja), a JavaScript engine
written in Go (the release stays `CGO_ENABLED=0`).

| Global | Meaning |
| --- | --- |
| `tools.<Name>(args)` | calls a tool with the arguments it takes as a direct call and returns the result as a string, for example `tools.Grep({pattern: "TODO"})`. Names are case-insensitive |
| `tools.parallel([[name, args], …])` | runs read-only calls at the same time and the rest in order, and returns an array of strings |
| `tools.list()`, `tools.search(query)`, `tools.describe(name)` | discovery: names, and argument shapes on demand |
| `mcp.list()`, `mcp.describe("server.tool")`, `mcp.<server>.<tool>(args)` | MCP tools ([below](#mcp-in-code-mode)). For a name with a hyphen, index it: `mcp["my-server"]["tool-name"]` |
| `print(...)`, `console.log(...)` | the only output |
| `JSON`, `Math`, `Date`, `RegExp` and the rest of the language | as in any JavaScript |

Not present: `require`, modules, `fetch`, `XMLHttpRequest`, `process`, a
filesystem, timers and anything else that reaches outside the interpreter.

Callable tools are `Read`, `Grep`, `Glob`, `Edit`, `Write`, `Bash`, `WebFetch`,
`WebSearch`, the native catalogue, remote tools (`GH`, `Glab`, `AWS`) and
`SearchFetched`, narrowed by the session's tool allowlist. Interactive,
planning, board, process-control and subagent tools, `Skill` and `Code` itself
are not. A script that errors returns the message with the line number so the
model can fix it in one retry.

## MCP in code mode

In code mode only, a script reaches MCP servers through the `mcp` global. This
costs nothing up front: no server's tool definitions are sent, and the model
finds tools with `mcp.list()` and `mcp.describe("server.tool")` when it needs
them.

MCP behaves exactly as before in agent, plan and goal mode: `mcp__server__tool`
definitions, the sealed tools briefing, deferral behind `ToolSearch`, plan mode
not offering them. The two paths share nothing except the server connections.
See [mcp.md](mcp.md).

## Security model

- **Every call in a script is a full tool call.** A script call goes through
  the session's own pipeline: tool surface and allowlist, argument checks, permission
  rules (a deny rule withholds it; an unmatched mutating call asks, once per call),
  hooks, the OS sandbox, the file-diff recorder, sanitising and the classifier.
  The script receives only the text that pipeline admitted, or the "tool result
  withheld" message. It cannot see content the classifier held back.
- **`Code` does not ask for itself.** The calls inside it do, so asking for the
  script as well would only ask twice. A deny rule on `Code` still stops the
  whole script.
- **The output is sanitise-only.** What a script prints is built from admitted
  text and the model's own literals, so `KindCode` is sanitised and not
  classified a second time. A long output is offloaded like other large results
  and read back with `ReadResult`.
- **MCP stays sealed.** `mcp.describe` returns the server's already sanitised
  and capped definition, classified as an MCP result before anyone reads it.
  Server text does not reach a script or the model unclassified.
- **Nested results are whole.** A call inside a script is not offloaded or
  recorded in the read index, because the script, not the model, reads it.
- **The interpreter is confined.** No ambient capabilities (above). A script
  is limited by wall-clock time, a nested call budget, a script size, an output
  cap, size guards on `repeat`, `padStart` and `padEnd`, and a sampled heap
  watchdog. goja has no hard memory limit, so the watchdog is a bound, not a
  guarantee.
- **Scope.** A script cannot start a subagent, ask the user, change the plan or
  touch the board.

## Settings

The `code` key in settings:

| Key | Default | Meaning |
| --- | --- | --- |
| `code.enabled` | `true` | turns code mode off: `/mode code` then runs as agent mode |
| `code.timeout_ms` | `120000` | wall-clock limit for one script (ceiling 600000) |
| `code.max_calls` | `50` | nested tool calls per script (ceiling 500) |
| `code.max_output_bytes` | `16384` | what one script may print (ceiling 262144) |

A project settings file may turn `code.enabled` off and lower a limit, never
turn code mode on or raise a limit.

## Limitations

- There is no TypeScript type-check (Pi has one). `tools.describe` gives the
  argument shapes, and every call's arguments are checked against the tool's
  schema when it runs.
- A script is synchronous. Use `tools.parallel` for concurrency.
- Calls that the classifier inspects (`Bash`, `Read`, `WebFetch`) cost the same
  classification time inside a script as outside. Prefer `Grep` and `Glob` for
  searching.
- Code mode is agent-style: it does not run the goal loop or plan review.
