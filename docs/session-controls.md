# Session controls

Session controls change how the running session works: its mode, its model,
the guardrails and ask gates, and what the transcript shows. Each control has a
slash command and most have a key. The same commands and keys work in the TUI
and in a web session started by `belai rc --web-controls`
([remote-control.md](remote-control.md#session-controls-from-the-web)),
because both are parsed by one table (`internal/sessionctl`).

| Control | Command | Key | Values |
| --- | --- | --- | --- |
| Mode | `/mode agent\|plan\|goal\|code\|auto` | `shift+tab`, `f5` | cycles agent, plan, goal, auto; `code` is chosen by name ([code-mode.md](code-mode.md)) |
| Model | `/model <provider> <model> [effort]` | `ctrl+q` | `ctrl+q` swaps between the main model and the fast tier |
| Reasoning effort | `/effort default\|<level>` | `f6` | the levels the model takes |
| Guardrails | `/guardrails on\|off` | `f3` | on, off |
| Ask | `/ask on\|off` | `f4` | on, off |
| Caveman | `/caveman on\|off` | `f2` | on, off |
| Auto-commit | `/autocommit on\|off` | | commits each completed goal |
| Reasoning display | `/reasoning auto\|shown\|hidden` | `ctrl+r` | auto follows `ui.show_reasoning` |
| Tool calls and edits | `/tools auto\|all\|edits\|none` | `ctrl+t` | auto follows `ui.show_tool_calls` and `ui.show_edits` |
| Decisions display | `/decisions hidden\|decisions\|security\|all` | | the `ui.show_internal_work` levels |
| Tests | `/tests post_end off\|goal\|goal_plan\|session`, `/tests on_fail off\|diagnose\|fix` | | the post-end test pass |
| Language servers | `/lsp on\|off [language]` | | the whole feature, or one language |
| Jev | `/jev job <name> on\|off`, `/jev threshold <key> <0..1>`, `/jev reset` | | the jobs and cut-offs in [jev-jobs.md](jev-jobs.md) |

`/model` and `/lsp` with no argument still open their screens.

## What a control changes

A control changes this session only. Auto-commit, decisions, tests, language
servers and Jev are kept as an overlay on the session's settings and applied
again after every settings reload, so a `/settings` edit does not undo them and
nothing is written to a settings file. `f2`, `f3` and `f4` in the TUI also save
the choice to the project's own preferences, as they always have; their
commands do the same work as their keys.

Every value is checked before it applies. A Jev threshold is held to the same
rules as in the settings file (the allow and deny cut-offs stay on opposite
sides of 0.5 and at least 0.05 apart), a job or a language must be a known one,
and a model must be one this host has credentials for. Tests take only the
trigger and the fail branch: the command and scope stay in your settings.
