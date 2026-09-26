# MCP servers

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

Signet can connect to Model Context Protocol servers and offer their tools to
the model next to its own. Each server tool appears as
`mcp__<server>__<tool>`, and every result is treated as untrusted third-party
text.

- [Configuring servers](#configuring-servers)
- [Project-proposed servers](#project-proposed-servers)
- [How tools appear](#how-tools-appear)
- [Security model](#security-model)
- [Commands](#commands)
- [Limitations](#limitations)

## Configuring servers

Servers are declared in your global `settings.json`:

```json
{
  "mcp": {
    "servers": {
      "github": {
        "transport": "stdio",
        "command": "github-mcp-server",
        "args": ["stdio"],
        "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "env:GITHUB_TOKEN"},
        "sandbox": true
      },
      "docs": {
        "transport": "http",
        "url": "https://mcp.example.com/mcp",
        "headers": {"Authorization": "credential:docs-mcp"}
      }
    }
  }
}
```

- `stdio` servers run as a child process. They start with the scrubbed
  environment, and only the variables you list in `env` are passed through.
  A value of `env:NAME` copies that variable; `credential:NAME` reads a stored
  credential.
- `http` servers use the streamable HTTP transport through Signet's HTTP
  client, so requests carry the usual trace headers and follow the AI Firewall
  setting.
- `sandbox: true` runs a stdio server under the [Bash sandbox](sandbox.md).

Servers start after the first-run trust gate and are stopped when the session
ends.

## Project-proposed servers

A repository may propose servers in `.vulnetix/settings.json`. Like
`workspace_dirs`, the proposal is dropped unless you set
`allow_project_mcp_servers` in your global settings, and then each server is
started only after you accept it by name in the trust prompt, which shows its
command or URL. A server the project adds later prompts again.

## How tools appear

- Name: `mcp__<server>__<tool>`, the convention models are trained on.
- Permission rules match the subject `<server>/<tool>`, so `Deny: github/*`
  removes a whole server and `Ask: github/create_*` asks for its write tools.
- Server tools are not available in plan mode, whatever the server says about
  them.

## Security model

- Tool results carry the `mcp` tool kind, which is always sanitized and always
  classified.
- Tool names and descriptions come from the server, so they are sanitized,
  length-capped, and listed inside the sealed tools briefing, never in the
  system block. A server cannot rename a built-in tool.
- Input schemas go through the same argument checker as built-in tools.
- The server command is never taken from the project layer without your
  acceptance.

## Commands

`/mcp` lists servers with their state (running, failed, disabled) and tool
count. From there you can view a server's tools, restart it, or disable it for
the session.

## Limitations

- Tools, prompts and resources: only tools are supported at first.
- A server that changes its tool list mid-session is picked up on the next
  turn.
