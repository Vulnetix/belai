# Belai in Zed

Zed runs external agents over the [Agent Client Protocol](https://agentclientprotocol.com).
This page adds `belai acp` as one of them. Zed's own reference is
[External Agents](https://zed.dev/docs/ai/external-agents).

## Before you start

1. Install Belai and check `belai -version` works in the shell the editor uses.
2. Trust the project directory once. Open it in the TUI and accept the prompt, or
   run `belai -trust-dir` in it. The trust prompt never runs over ACP, so an
   untrusted directory fails at `session/new` with a message saying so.
3. Have a provider ready. `belai acp` resolves the provider and model like the
   TUI does (settings, `BELAI_PROVIDER`, available credentials). To pin them,
   add `-provider <name> -model <id>` to the arguments below.

## Add the agent

Open Zed's `settings.json` (`zed: open settings` in the command palette) and add
an entry under `agent_servers`:

```json
{
  "agent_servers": {
    "Belai": {
      "type": "custom",
      "command": "belai",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

To pick a provider and model:

```json
"args": ["acp", "-provider", "anthropic", "-model", "claude-sonnet-5-5"]
```

Zed starts `belai` from its own environment. If Zed was launched from a desktop
launcher that does not see your shell `PATH`, use the absolute path from
`which belai` as `command`.

## Start a thread

1. Open the Agent Panel.
2. Use the agent selector, or the new-thread menu, and choose **Belai**.
3. Type a prompt. The first prompt starts a session in the open project folder.

To bind a key, add a binding to `"agent: new external agent thread"` in
`keymap.json`.

## Reading the logs

Run `dev: open acp logs` from the command palette to see the protocol messages
between Zed and Belai. Belai writes nothing but protocol messages to stdout, so
its own diagnostics are on stderr.

## Zed features that do not carry over

Zed Agent profiles, Skills and native agent instructions belong to Zed's own
agent. Belai reads its own settings, skills, hooks and `AGENTS.md`.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `session/new` fails and mentions trust | Run `belai -trust-dir` in the project directory. |
| The agent does not appear in the selector | Check the JSON is valid and the key sits under `agent_servers`. |
| "command not found" | Use the absolute path to `belai` as `command`. |
| A permission ask never arrives | Check the ACP logs; an unanswered ask denies. |

## What you get

Reply and reasoning text stream into the chat. Each tool call appears as it
starts, edits arrive with their diff, and the goal-mode plan shows as a todo
list. Permission asks come to the editor with allow once, allow for this
session, and reject. "Allow for this session" covers that tool name only and
ends with the session. Every classifier, permission rule, sandbox and budget
applies as in a headless run; see [Security model](acp.md#security-model).

Sessions are alpha: they are not in the Belai session store, clarifying
questions are answered as declined, and images and audio are not accepted. See
[Limitations](acp.md#limitations).
