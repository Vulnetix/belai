# MCP servers

**Status:** alpha-20260926. Shipped in an early form; the settings may still
change.

Belai connects to Model Context Protocol servers and offers their tools to
the model next to its own. Each server tool appears as
`mcp__<server>__<tool>`, and every result is treated as untrusted third-party
text.

- [Configuring servers](#configuring-servers)
- [How tools appear](#how-tools-appear)
- [Where tools are offered](#where-tools-are-offered)
- [Security model](#security-model)
- [Built-in servers](#built-in-servers)
- [Commands](#commands)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## Configuring servers

Servers are declared in your global `settings.json`
(`~/.vulnetix/belai/settings.json`):

```json
{
  "mcp": {
    "servers": {
      "github": {
        "transport": "stdio",
        "command": "github-mcp-server",
        "args": ["stdio"],
        "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "env:GITHUB_TOKEN"},
        "tools": ["get_issue", "list_issues"],
        "sandbox": true
      },
      "docs": {
        "transport": "http",
        "url": "https://mcp.example.com/mcp",
        "headers": {"Authorization": "env:DOCS_MCP_AUTH"},
        "timeout_ms": 30000
      }
    }
  }
}
```

| Key | Meaning |
| --- | --- |
| `transport` | `stdio` (default) or `http` (MCP streamable HTTP, JSON or event-stream responses) |
| `command`, `args` | start a stdio server |
| `env` | variables for a stdio server, on top of the scrubbed environment. `env:NAME` copies `NAME` from Belai's environment; `cred:KEY` reads `KEY` from the credential Belai stored for this server (keychain, else the global credentials file) |
| `url`, `headers` | reach an http server. A header value `env:NAME` is read from the environment. `"Authorization": "vulnetix:cli"` sends the Vulnetix CLI's credential, and only to `https://*.vulnetix.com` (`/vulnetix mcp` writes this entry; see [vulnetix.md](vulnetix.md)) |
| `tools` | offer only these server tools |
| `sandbox` | run a stdio server under the [OS sandbox](sandbox.md) |
| `timeout_ms` | per-call timeout, default 60000, at most 600000 |
| `disabled` | keep the server configured but do not start it |
| `secrets` | names, never values: binds a credential key (read by a `cred:KEY` value) to a Secrets Vault entry the library pushes to this host when it installs the server |

Server names are letters, digits, `_` and `-`, at most 32.

Servers start in the background once the first-run trust gate has passed, so
a slow server never delays startup. They stop when Belai exits. Connecting
(the handshake and the tool listing together) gets 30 seconds. A server that
fails to start, or does not connect in time, is reported by `/mcp` and offers
no tools; the session carries on without it. A one-shot `-prompt` run waits for every server to connect or
fail before its turn.

The `mcp` key is read from your global settings only. A repository's
`.vulnetix/settings.json` cannot add, change or start a server.

## How tools appear

- Name: `mcp__<server>__<tool>`, the convention models are trained on,
  restricted to letters, digits, `_` and `-` and 64 characters.
- Description: the server's text, sanitized, flattened and capped at 1024
  characters, prefixed with a line naming the server and saying its results
  are untrusted.
- Arguments: the input schema's structure (types, nested properties, items,
  required keys, string enums) with sanitized, capped descriptions.
  Arguments the schema does not declare are rejected before the call.
- Permission rules match the full tool name, for example
  `"allow": ["mcp__github__get_issue"]`. Without an allow rule every call
  asks, because a server tool may do anything.
- Server tools are mutating, so they are not offered in plan mode. The built-in
  decision tools only read and are ([where they are offered](#where-tools-are-offered)).
- An agent profile's `tools` allowlist keeps a server tool only when it names it
  as `mcp__<server>__<tool>`. A fleet worker and a web session engaged with a
  profile also expand `mcp__<server>__*` to every tool of that server; a
  profile engaged in the TUI matches names exactly, so list each tool there.
- In [code mode](code-mode.md) a server tool is not advertised: a script calls
  it as `mcp.<server>.<tool>(args)`, with the same permission rules and
  classification. Agent, plan and goal mode are unchanged.

## Where tools are offered

A server tool is a mutating tool (kind `mcp`), so every surface that removes
mutating tools removes it too. The built-in decision server's tools (kind
`decision`) only read, so they stay on those surfaces. Only the main session's
model is offered MCP tools.

| Surface | Server tools (kind `mcp`) | Decision tools (kind `decision`) |
| --- | --- | --- |
| Agent mode, goal mode, an approved plan's execute turn and a fan-out turn | offered | offered |
| Plan mode, and agent mode with `read_only` on | not offered | offered |
| Code mode | not advertised; a script calls `mcp.<server>.<tool>()` | advertised, and a script calls `tools.mcp__clef__<tool>()` |
| Task, Explore, handoff and recovery subagents, and background agents | never offered | never offered |
| The classifier and the Jev jobs | none: those turns carry no tools | none |

- With `defer_tools` on (the default) the definitions are not in the core tool
  set. The sealed tools briefing names them and `ToolSearch` loads one into later
  requests.
- `belai agent run` starts the MCP servers only for a profile whose `tools` names
  an `mcp__` tool, because an MCP tool runs outside the worker's worktree.
- A server tool asks on every call unless an allow rule covers it, so a session
  that cannot ask (a fleet worker) can use one only through an allow rule. A
  decision tool never asks: it changes nothing. A Deny or Block rule on its name
  still withholds it.

## Security model

- Results carry their own tool kind, `mcp`, which is always sanitized and
  always classified before the model reads them. Images and other binary
  content are named, never inlined, and a result is capped at 64 KiB.
- A server's names and descriptions reach the model only through the sealed
  tools briefing, never the system block, after sanitizing and capping. A
  server cannot take the name of a built-in tool: every name starts with
  `mcp__`.
- A stdio server starts with the scrubbed environment (no `*_API_KEY`,
  `*_TOKEN`, `*_SECRET`, `BELAI_*` from your shell), gets only the variables
  you list, and runs in its own process group. With `sandbox: true` it runs
  under the OS sandbox too.
- A profile's [facts](agent-profiles.md#facts) are not passed to MCP servers. A
  server that needs a credential gets it through its own `env` entry.
- Belai offers servers nothing to call back: no roots, sampling or
  elicitation. It only answers `ping`.
- The `vulnetix:cli` header reference is resolved when the server is dialled
  and held in memory only. It expands only in the `Authorization` header of
  an `https` URL on `vulnetix.com` or a subdomain; any other use fails the
  server with the reason, so a hand-edited entry cannot send the credential
  elsewhere.

## Built-in servers

Belai offers two servers of its own. Neither comes from `mcp.servers`: a
settings entry named `clef` is ignored, `/mcp` lists `clef` with the transport
`builtin`, and nothing can replace or remove it. Both are switched under the
`mcp.builtin` key of your global settings, which a repository's settings cannot
set, and from the **MCP** section of `/model`.

| Key | Meaning |
| --- | --- |
| `mcp.builtin.clef.enabled` | Offer the decision server. Default `true`. |
| `mcp.builtin.clef.skip_ask` | Let the decision model answer an ask when it is confident. Default `true`. |
| `mcp.builtin.clef.skip_ask_at` | The confidence at or above which its answer is used. Above 0.5 and at most 1. Default 0.90. |
| `mcp.builtin.vulnetix.enabled` | Offer the hosted Vulnetix server while Vulnetix credentials exist. Default `true`. |

### The decision server (`clef`)

`clef` is compiled into every Belai build. It runs inside Belai on an in-process
pipe speaking the same JSON-RPC as a stdio server, so it goes through the same
client, the same tool naming (`mcp__clef__<tool>`), the same sanitizing and the
same 64 KiB result cap. Its tools ask a decision model for a true/false answer, an
enum pick, weights or an ordering, and return numbers and the caller's own
options with the model's confidence; the model then decides what to do with them.

- **When it is offered.** The switch is on and a decision backend can serve it:
  the Pix Sandbox build always has one (its Worker), and any other build needs your
  own Cloudflare Workers AI credentials (an account id and an API token, set with
  `/providers` or the environment). Otherwise `/mcp` and `/model` list it as
  disabled and say why. A login or a settings change is picked up without a restart.
- **Which backend answers.** Your classifier when it is a SystemOne backend (Clef on
  Workers AI directly or through AI Gateway, Strands Decider-2B, TypeSafe, Ollama's
  Tev1 or a `systemone` provider profile); otherwise Clef-flash on Cloudflare
  Workers AI from your credentials. A local decision model (`decision-local`, Clef
  included), OpenRouter Decisions and Tev1 on Together are not used for it. A call
  that finds no backend returns a tool error. The question, its context and the
  options leave the machine for that backend, cleaned for a decision request first.
- **Read-only.** Its tools have kind `decision`: no ask on any call, offered in plan
  mode and with `read_only`, run concurrently, and advertised directly in code mode.
  A result is sanitized and not classified, because the harness composes all of it
  from numbers and your model's own option text; the backend's own text never
  reaches it. See [where tools are offered](#where-tools-are-offered).
- It is not a Jev job and does not use `jev.jobs`.
- **What the model is told.** While the tools are on the session's surface, the system
  prompt carries a short "Decision tools" section (`internal/prompt/decisiontools.go`):
  what the decision model is (numbers back, never prose; it sees only what is sent),
  when to reach for it instead of reasoning to a pick (a yes/no gate, one pick from a
  list, how close the options are, an order or shortlist, several questions about the
  same facts), why (faster and steadier, and it reports how sure it is), how to read
  the answer (act on a high confidence; on a low one or an "uncertain" gate, gather
  facts or ask), and what not to use it for (anything reading or running a command
  settles, or a safety call a rule already answers). The section is absent when the
  tools are not offered and for an explore subagent.

The tools, their limits and the definitions the model sees are in
[Pix Sandbox](pix-sandbox.md#the-decider-mcp).

### Asks answered by the decision model

When the harness would ask you something, `clef` can answer first. Above the
threshold its answer is used at once; at or below it, or on any failure, you are
asked exactly as before.

| Ask | What the model is asked | Used when |
| --- | --- | --- |
| A tool permission ask | one proposition: whether to let this tool call run, given the tool, its target (a command line, a path, a URL) and your request. It never sees the call's arguments or a diff | confidence at or above `skip_ask_at`: a yes runs the call, a no withholds it |
| `AskUserQuestion` and clarifying questions | one choice per single-select group | the top option's weight is at or above `skip_ask_at`; groups below it still go to you and the answers merge |
| The mode choice | the same choice over its options | as above |
| The plan review | one choice over approve here, approve in a new session and keep planning, given your request, the plan's title and its step count (never its text). Refine needs your notes, so it is never chosen | as above, once the plan turn has finished and only while the review is still open and unanswered |

- It runs only where someone could be asked. A headless run, a background agent and
  a subagent never ask, so it never runs there.
- A decision never writes an allow rule: "always allow" is only ever your choice.
- Deny and Block rules, hooks, the OS sandbox and the classifier still apply to
  everything it lets through.
- The transcript records each one as an ask answered by `clef` with its confidence
  and no question text, and the thread says so. The model is told a decided answer
  was not the user's.
- Turn it off with `skip_ask` or set the bar with `skip_ask_at` (the **Skip ask at
  threshold** row of `/model`, "off" or 0.70 to 0.99).

### The hosted Vulnetix server

While Vulnetix credentials exist and `mcp.builtin.vulnetix.enabled` is not
`false`, Belai offers the hosted Vulnetix MCP server, authenticated with the
credential by reference (see the `vulnetix:cli` rule under Security model).
Nothing is written to your settings; an `mcp.servers.vulnetix` entry of your own
takes its place. `/vulnetix mcp remove` and the switch turn it off.

## Secrets

A server's env and headers hold references, never secret values. A value is one of:

| Value | Meaning |
| --- | --- |
| `env:NAME` | copy `NAME` from Belai's environment |
| `cred:KEY` | the secret stored for this server under `KEY`, in the keychain or, when there is none, your global credentials file. Only your own user credentials are read, never the environment, a repository file or netrc |
| `vault:NAME` | an entry of the Secrets Vault lease of a Pix Sandbox, in the headers of an http server only |
| `vulnetix:cli` | the Vulnetix CLI's credential, as described under Security model |
| plain text | allowed only for a name that does not read as a secret (a name containing key, token, secret, password, credential, private, auth or cookie needs a reference) |

References resolve when the server is dialled and are never written back to the
settings. A reference that cannot resolve fails the server with the reason; it is
never sent as text. A result that echoes a vault value is scrubbed like a command's
output.

A server the library installs may bind a `cred:KEY` to a vault entry
(`secrets`): installing it on a self-hosted host asks the library to send that
entry's value once over TLS, and Belai stores it where `cred:KEY` reads it.

## Commands

`/mcp` opens the MCP screen (also `f1`, then `c`). **Built-in** has the same
switches as the MCP group of `/model`. **Servers** lists each server of your
global settings as local (a command run on this machine) or remote (a URL) with its
state and tool count, and a form to add or edit one: transport, command and
arguments or URL, env or headers as `NAME=value` pairs, a tool allowlist, a
timeout, the OS sandbox for a local server, and one secret to store under a key you
name (kept in the keychain, referenced by `cred:KEY`). Space switches a server off or
on, `r` restarts it, `x` (twice) deletes it and the secrets it referenced. The form
refuses what the library would: a literal secret, a plain http URL that is not
loopback, a duplicate name and the name `clef`.

`/mcp list` prints every server with its transport, state (running, failed with
the reason, or disabled) and tools. `/mcp restart <name>` reconnects one; the next
turn uses its current tools.

## Limitations

- Only tools are supported; server prompts and resources are not.
- A server that connects after a TUI session was built, or changes its tool
  list, is picked up when the session is rebuilt (`/clear`, or a mode or
  model change).

## Edge cases

- Two server tools whose names become the same after sanitizing: the one
  the server lists first is offered, the other is dropped.
- A name longer than 64 characters is cut at 64.
- A schema property whose name is not letters, digits, `_` and `-` is
  dropped, and `required` keeps only properties that survived.
- A property type Belai does not know becomes `string`; a union such as
  `["integer", "null"]` takes its first known type.
- A server name with other characters fails to start and says why.
- A tool listed in `tools` that the server does not offer is ignored.
- An http server answering with a non-2xx status fails that call with the
  status. During connection, the status and a short excerpt of the body are
  the reason `/mcp` shows.
- An http server's response is read up to 16 MiB; a larger one cannot be
  decoded and fails the call.
- A stdio server still running two seconds after Belai closes its input is
  killed with its process group.
