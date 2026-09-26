# Editor integration (ACP)

**Status:** alpha-20260926. Shipped in an early form; the supported methods
may still change.

`signet acp` runs Signet as an [Agent Client
Protocol](https://agentclientprotocol.com) agent on stdin and stdout. Editors
that speak ACP (Zed, JetBrains IDEs, Neovim through a plugin) can then use
Signet as their agent, with the same classifier, permission rules, guardrails
and budgets as the TUI.

- [Editor setup](#editor-setup)
- [What is supported](#what-is-supported)
- [Security model](#security-model)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

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

Other editors take the same command. `signet acp -provider <name> -model <id>`
picks a provider and model; otherwise they resolve as they do for the TUI
(settings, `SIGNET_PROVIDER`, available credentials). Everything else comes
from your normal Signet settings for the project directory.

## What is supported

| ACP method or update | Signet behaviour |
| --- | --- |
| `initialize` | protocol version 1; embedded file context accepted; no session loading |
| `session/new` | starts a session in the editor's project directory |
| `session/prompt` | runs one turn; text, file links and embedded file text become the prompt |
| `session/cancel` | stops the turn; the prompt returns `cancelled` |
| `agent_message_chunk` | streamed reply text |
| `agent_thought_chunk` | streamed reasoning |
| `tool_call` | each tool call as it starts, with its kind (`read`, `edit`, `search`, `execute`, `fetch`, `think`, `other`) and arguments |
| `tool_call_update` | the file diff a call made, then its result, `completed` or `failed` |
| `plan` | the goal-mode todo list |
| `session/request_permission` | a permission ask, offering allow once, allow for this session, or reject |

A turn stops with `end_turn`, `cancelled`, or `refusal` when the prompt
classifier refuses it.

## Security model

- The project directory must already be trusted. `session/new` in an
  untrusted directory fails with a message telling you to open it in the TUI
  once or run `signet -trust-dir` there. The trust prompt never runs over ACP.
- Each ACP session is built the way the headless CLI builds one: the same
  classifier, posture gates, permission rules, sandbox and token budgets. The
  guardrails switch follows your settings for that directory.
- Permission asks go to the editor. "Allow for this session" is remembered in
  memory for that session and tool only, and never written to a settings
  file. Any error, cancellation or other answer denies.
- MCP servers come from your global settings. Servers an editor offers in
  `session/new` are ignored.

## Limitations

- Sessions last as long as the editor's connection and are not written to
  the Signet session store, so they cannot be resumed from the TUI.
- Clarifying questions are not asked over ACP; the agent proceeds with its
  best reading of the prompt.
- Images and audio in prompts are not accepted.
- Slash commands, modes and the TUI panels are not exposed.

## Edge cases

- A second `session/prompt` on a session whose turn is still running is
  refused.
- An empty prompt, an unknown session id, or a relative `cwd` is refused
  with invalid-params.
- A permission ask the editor fails to answer, answers with `cancelled`, or
  answers with an unknown option denies.
- "Allow for this session" covers that tool name only, and ends with the
  session.
- Cancelling a turn returns `cancelled` even when the turn also failed.
