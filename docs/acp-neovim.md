# Belai in Neovim

Neovim reaches ACP agents through plugins. This page uses
[CodeCompanion](https://github.com/olimorris/codecompanion.nvim); other ACP
plugins such as [avante.nvim](https://github.com/yetone/avante.nvim),
[agentic.nvim](https://github.com/carlos-algms/agentic.nvim) and
[hermes.nvim](https://github.com/Ruddickmg/hermes.nvim) take the same command,
`belai acp`, in their own agent settings.

## Before you start

1. Install Belai and check `belai -version` works in the shell the editor uses.
2. Trust the project directory once. Open it in the TUI and accept the prompt, or
   run `belai -trust-dir` in it. The trust prompt never runs over ACP, so an
   untrusted directory fails at `session/new` with a message saying so.
3. Have a provider ready. `belai acp` resolves the provider and model like the
   TUI does (settings, `BELAI_PROVIDER`, available credentials). To pin them,
   add `-provider <name> -model <id>` to the arguments below.

## Add the adapter (CodeCompanion)

Define Belai as a custom ACP adapter in your `setup`:

```lua
require("codecompanion").setup({
  adapters = {
    acp = {
      belai = function()
        local helpers = require("codecompanion.adapters.acp.helpers")
        return {
          name = "belai",
          formatted_name = "Belai",
          type = "acp",
          roles = { llm = "assistant", user = "user" },
          commands = {
            default = { "belai", "acp" },
          },
          defaults = { mcpServers = {}, timeout = 20000 },
          parameters = {
            protocolVersion = 1,
            clientCapabilities = {
              fs = { readTextFile = true, writeTextFile = true },
            },
            clientInfo = { name = "CodeCompanion.nvim", version = "1.0.0" },
          },
          handlers = {
            setup = function(self) return true end,
            auth = function(self) return true end,
            form_messages = function(self, messages, capabilities)
              return helpers.form_messages(self, messages, capabilities)
            end,
            on_exit = function(self, code) end,
          },
        }
      end,
    },
  },
  strategies = {
    chat = { adapter = "belai" },
  },
})
```

To pick a provider and model, use
`default = { "belai", "acp", "-provider", "anthropic", "-model", "claude-sonnet-5-5" }`.
Belai ignores the `mcpServers` a client offers and uses the servers in your
global Belai settings.

Start Neovim inside the project directory, since the session's working
directory is the one the plugin reports.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| The chat hangs on connect | Run `belai acp` in a terminal; it should wait silently on stdin. Raise `timeout` if start-up is slow. |
| `session/new` fails and mentions trust | Run `belai -trust-dir` in the project directory. |
| Plugin options differ from the above | Plugins change; the fixed part is the command, `belai acp`. |

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
