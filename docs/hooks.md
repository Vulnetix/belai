# Hooks

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

Hooks run a command of yours at fixed points in a session: before and after a
tool call, when you submit a prompt, when a turn ends, before compaction, and
when Signet needs your attention. A hook can log, lint, veto a tool call, or
hand the model a short note.

- [Events](#events)
- [Hook files](#hook-files)
- [The stdin and stdout contract](#the-stdin-and-stdout-contract)
- [Security model](#security-model)
- [Settings](#settings)
- [Limitations](#limitations)

## Events

| Event | Fires | Can block |
| --- | --- | --- |
| `session_start` | the TUI opens or resumes a session | no |
| `session_end` | the session closes | no |
| `user_prompt_submit` | a prompt is submitted, before the turn starts | yes |
| `pre_tool` | before any tool call runs, after the permission rules | yes |
| `post_tool` | after a tool call returns | no |
| `pre_edit` | before a `Write` or `Edit` runs | yes |
| `post_edit` | after a `Write` or `Edit` succeeds | no |
| `stop` | a turn ends (agent, plan, or goal pass) | no |
| `pre_compact` | before `/compact` or automatic compaction | no |
| `subagent_stop` | an explore or background subagent finishes | no |
| `notification` | Signet is waiting on you (see [notifications](notifications.md)) | no |

## Hook files

Each hook is one JSON file in `~/.vulnetix/signet/hooks/` (or
`$SIGNET_HOME/hooks/`). Plugins can ship hooks too (see [plugins](plugins.md)).
There is no project-level hooks directory: a repository never supplies a
command Signet will run.

```json
{
  "name": "go-vet-after-edit",
  "event": "post_edit",
  "matcher": "Edit|Write",
  "command": "bin/vet.sh",
  "timeout_ms": 5000
}
```

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | yes | unique name, shown in the transcript |
| `event` | yes | one of the events above |
| `command` | yes | a path relative to the hooks directory, then arguments. No shell, no metacharacters, no `..` |
| `matcher` | no | tool-name pattern (`Bash`, `Edit\|Write`, `mcp__*`) for the tool events |
| `timeout_ms` | no | default 5000, capped at 60000 |

Any other key fails validation. An invalid file is reported and skipped, under
the `hook_invalid` posture gate.

## The stdin and stdout contract

The hook receives one JSON object on stdin:

```json
{
  "event": "pre_tool",
  "session_id": "…",
  "cwd": "/home/me/project",
  "tool_name": "Bash",
  "tool_input": {"command": "rm -rf build"},
  "tool_result_summary": null
}
```

It may print one JSON object on stdout:

```json
{"decision": "deny", "reason": "build/ is managed by make", "additional_context": ""}
```

- `decision` is `allow`, `deny` or `ask`. It is only read from blocking
  events. Omitted means no opinion.
- `reason` is shown to you and, for a denial, returned to the model as the
  tool result.
- `additional_context` is a short note for the model.

A non-zero exit from a blocking hook counts as `deny`. So does a timeout, or
stdout that is not valid JSON. A non-zero exit from a non-blocking hook is
reported and otherwise ignored.

## Security model

- A hook cannot widen permissions. `allow` from a hook never overrides a
  `deny` or `block` permission rule, and `ask` still asks.
- `additional_context` and a denial `reason` are text a program you did not
  necessarily write produced, so they carry a dedicated tool kind (`hook`)
  that is always sanitized and always classified. They ride back as a sealed
  attachment on the next turn, never in the system block.
- Commands run without a shell, from the hooks directory, with the scrubbed
  environment used for Bash (no `*_API_KEY`, `*_TOKEN`, `*_SECRET`,
  `SIGNET_*`), plus the `SIGNET_SESSION_ID` and `TRACEPARENT` identity
  variables. A command path that resolves outside the hooks directory,
  including through a symlink, is refused.
- Hooks are your configuration, so they run with guardrails on or off. Only
  the classification of their output follows the guardrails switch.

## Settings

```json
{
  "hooks": {
    "enabled": true
  }
}
```

The project layer may set `hooks.enabled` to `false`, never to `true`.

## Limitations

- Hooks are read at session start. Adding a file needs a new session.
- Output is capped at 64 KiB, and `additional_context` at 2 KiB.
