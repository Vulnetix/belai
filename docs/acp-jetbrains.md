# Belai in JetBrains IDEs

JetBrains IDEs with the AI Assistant plugin can run any ACP agent. This page
adds `belai acp`. JetBrains' reference is
[Agent Client Protocol](https://www.jetbrains.com/help/ai-assistant/acp.html).

## Before you start

1. Install Belai and check `belai -version` works in the shell the editor uses.
2. Trust the project directory once. Open it in the TUI and accept the prompt, or
   run `belai -trust-dir` in it. The trust prompt never runs over ACP, so an
   untrusted directory fails at `session/new` with a message saying so.
3. Have a provider ready. `belai acp` resolves the provider and model like the
   TUI does (settings, `BELAI_PROVIDER`, available credentials). To pin them,
   add `-provider <name> -model <id>` to the arguments below.

## Add the agent

1. Open the AI Chat panel.
2. Click the three-dots menu at the top right and choose **Add Custom Agent**.
   The IDE creates and opens `~/.jetbrains/acp.json`. You can also edit that
   file directly.
3. Register Belai:

```json
{
  "agent_servers": {
    "Belai": {
      "command": "/full/path/to/belai",
      "args": ["acp"],
      "env": {}
    }
  }
}
```

Use the absolute path from `which belai`. An IDE started from a launcher often
does not inherit your shell `PATH`, so a bare `belai` may not be found.

To pick a provider and model, extend `args`:
`["acp", "-provider", "anthropic", "-model", "claude-sonnet-5-5"]`.

## Use it

Belai appears in the AI Chat agent selector. Open the project first, since the
project folder becomes the session's working directory, and it must be trusted
in Belai (see above).

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| The agent is missing from the selector | Restart the IDE after editing `acp.json`, and check the JSON is valid. |
| "command not found" | Use the absolute path to `belai`. |
| `session/new` fails and mentions trust | Run `belai -trust-dir` in the project directory. |

## What you get

Reply and reasoning text stream into the chat. Each tool call appears as it
starts, edits arrive with their diff, and the goal-mode plan shows as a todo
list. Permission asks come to the editor with allow once, allow for this
session, and reject. "Allow for this session" covers that tool name only and
ends with the session. Every classifier, permission rule, sandbox and budget
applies as in a headless run; see [Security model](acp.md#security-model).

Sessions are alpha: an editor cannot load an earlier one back, clarifying
questions are answered as declined, and audio is not accepted (images are). See
[Limitations](acp.md#limitations).
