# Belai in VS Code

VS Code has no built-in ACP client; extensions provide one. This page uses
[ACP Client](https://github.com/formulahendry/vscode-acp). Other ACP extensions
take the same command, `belai acp`.

## Before you start

1. Install Belai and check `belai -version` works in the shell the editor uses.
2. Trust the project directory once. Open it in the TUI and accept the prompt, or
   run `belai -trust-dir` in it. The trust prompt never runs over ACP, so an
   untrusted directory fails at `session/new` with a message saying so.
3. Have a provider ready. `belai acp` resolves the provider and model like the
   TUI does (settings, `BELAI_PROVIDER`, available credentials). To pin them,
   add `-provider <name> -model <id>` to the arguments below.

## Add the agent

Add Belai to the `acp.agents` setting in `settings.json`:

```json
{
  "acp.agents": {
    "Belai": {
      "command": "belai",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

To pick a provider and model, use
`"args": ["acp", "-provider", "anthropic", "-model", "claude-sonnet-5-5"]`.

You can also open the ACP Client panel in the Activity Bar and use **+** to add
the same entry.

## Connect

1. Open the project folder in VS Code.
2. Open the ACP Client panel and click **Belai** to connect.
3. Chat in the panel. `Ctrl+Shift+A` (`Cmd+Shift+A` on macOS) opens it.

The extension keeps one agent active at a time.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| Connecting fails with "command not found" | VS Code was launched without your shell `PATH`; use the absolute path to `belai`. |
| `session/new` fails and mentions trust | Run `belai -trust-dir` in the project directory. |
| Setting names differ | Extensions change; the fixed part is the command, `belai acp`. |

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
