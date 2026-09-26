# MCP servers

**Status:** alpha-20260926. Shipped in an early form; the settings may still
change.

Signet connects to Model Context Protocol servers and offers their tools to
the model next to its own. Each server tool appears as
`mcp__<server>__<tool>`, and every result is treated as untrusted third-party
text.

- [Configuring servers](#configuring-servers)
- [How tools appear](#how-tools-appear)
- [Security model](#security-model)
- [Commands](#commands)
- [Limitations](#limitations)

## Configuring servers

Servers are declared in your global `settings.json`
(`~/.vulnetix/signet/settings.json`):

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
| `env` | variables for a stdio server, on top of the scrubbed environment. `env:NAME` copies `NAME` from Signet's environment |
| `url`, `headers` | reach an http server. A header value `env:NAME` is read from the environment |
| `tools` | offer only these server tools |
| `sandbox` | run a stdio server under the [OS sandbox](sandbox.md) |
| `timeout_ms` | per-call timeout, default 60000, at most 600000 |
| `disabled` | keep the server configured but do not start it |

Server names are letters, digits, `_` and `-`, at most 32.

Servers start in the background once the first-run trust gate has passed, so
a slow server never delays startup. They stop when Signet exits. A server that
fails to start is reported by `/mcp` and offers no tools; the session carries
on without it. A one-shot `-prompt` run waits for every server to connect or
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
- Server tools are not offered in plan mode, and an agent profile's tool
  allowlist drops them.

## Security model

- Results carry their own tool kind, `mcp`, which is always sanitized and
  always classified before the model reads them. Images and other binary
  content are named, never inlined, and a result is capped at 64 KiB.
- A server's names and descriptions reach the model only through the sealed
  tools briefing, never the system block, after sanitizing and capping. A
  server cannot take the name of a built-in tool: every name starts with
  `mcp__`.
- A stdio server starts with the scrubbed environment (no `*_API_KEY`,
  `*_TOKEN`, `*_SECRET`, `SIGNET_*` from your shell), gets only the variables
  you list, and runs in its own process group. With `sandbox: true` it runs
  under the OS sandbox too.
- Signet offers servers nothing to call back: no roots, sampling or
  elicitation. It only answers `ping`.

## Commands

`/mcp` lists every configured server with its transport, state (running,
failed with the reason, or disabled) and tools. `/mcp restart <name>`
reconnects one; the next turn uses its current tools.

## Limitations

- Only tools are supported; server prompts and resources are not.
- A server that connects after a TUI session was built, or changes its tool
  list, is picked up when the session is rebuilt (`/clear`, or a mode or
  model change).
- Agent profiles cannot name MCP tools in their allowlist yet.
