# Editor integration (ACP)

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

`signet acp` runs Signet as an [Agent Client
Protocol](https://agentclientprotocol.com) server on stdin and stdout. Editors
that speak ACP (Zed, JetBrains IDEs, Neovim through a plugin) can then use
Signet as their agent, with the same classifier, permission rules and
guardrails as the TUI.

- [Editor setup](#editor-setup)
- [What is supported](#what-is-supported)
- [Security model](#security-model)
- [Limitations](#limitations)

## Editor setup

Zed, in `settings.json`:

```json
{
  "agent_servers": {
    "Signet": {
      "command": "signet",
      "args": ["acp"]
    }
  }
}
```

Other editors take the same command. Provider, model and settings come from
your normal Signet configuration.

## What is supported

| ACP method or update | Signet behaviour |
| --- | --- |
| `initialize` | advertises prompt, session load and permission support |
| `session/new` | starts a session in the editor's project directory |
| `session/load` | resumes a stored Signet session by id |
| `session/prompt` | runs a turn |
| `session/cancel` | stops the turn and keeps the partial result |
| `agent_message_chunk` | streamed reply text |
| `agent_thought_chunk` | streamed reasoning |
| `tool_call`, `tool_call_update` | each tool call, its progress, result and file diff |
| `plan` | the todo list |
| `session/request_permission` | a permission ask. `allow once` allows the call; `allow always` adds an allow rule for this session only |

Sessions are stored in the same store as TUI sessions, so `signet -r <id>`
opens a session the editor started.

## Security model

- The project directory must already be trusted. `session/new` in an
  untrusted directory fails with a message telling you to open it in the TUI
  once or run with `-trust-dir`. The trust prompt never runs over ACP.
- The classifier, posture gates, permission rules and token budgets apply as
  they do in the TUI. The guardrails switch follows your settings and
  `-guardrails`.
- `allow always` never writes a permission rule to disk.

## Limitations

- Clarifying questions are not supported over ACP; the agent proceeds with
  its best reading of the prompt.
- Slash commands and the TUI panels are not exposed.
