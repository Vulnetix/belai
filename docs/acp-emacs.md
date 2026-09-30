# Belai in Emacs

[agent-shell](https://github.com/xenodium/agent-shell) is an Emacs shell for ACP
agents. This page runs `belai acp` as a custom agent in it.

## Before you start

1. Install Belai and check `belai -version` works in the shell the editor uses.
2. Trust the project directory once. Open it in the TUI and accept the prompt, or
   run `belai -trust-dir` in it. The trust prompt never runs over ACP, so an
   untrusted directory fails at `session/new` with a message saying so.
3. Have a provider ready. `belai acp` resolves the provider and model like the
   TUI does (settings, `BELAI_PROVIDER`, available credentials). To pin them,
   add `-provider <name> -model <id>` to the arguments below.

## Add the agent

In your init file:

```elisp
(setq agent-shell-custom-acp-command '("belai" "acp"))
```

To pick a provider and model:

```elisp
(setq agent-shell-custom-acp-command
      '("belai" "acp" "-provider" "anthropic" "-model" "claude-sonnet-5-5"))
```

If Emacs does not see your shell `PATH` (common for a GUI Emacs on macOS), use
the absolute path from `which belai` as the first element, or fix
`exec-path`.

Belai reads its credentials from its own settings and keychain. If you would
rather pass a key through the environment, set it with
`agent-shell-make-environment-variables`.

## Start it

Run `M-x agent-shell` and pick the custom agent, or set it as the default with
`agent-shell-preferred-agent-config`. Open the shell from a buffer inside the
project, since that directory becomes the session's working directory.

The option names above come from agent-shell's own documentation and can change
between releases; the fixed part is the command, `belai acp`.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| "Searching for program: No such file" | Use the absolute path to `belai`. |
| `session/new` fails and mentions trust | Run `belai -trust-dir` in the project directory. |
| Nothing happens after the prompt | Check `*Messages*` and run `belai acp` in a terminal to see stderr. |

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
