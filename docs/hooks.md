# Hooks

**Status:** alpha-20260926. Shipped in an early form; the contract may still
change.

Hooks run a command of yours at fixed points in a session: before and after a
tool call, when you submit a prompt, when a turn ends, before compaction, and
when Belai needs your attention. A hook can log, lint, veto a tool call, or
hand the model a short note.

- [Events](#events)
- [Hook files](#hook-files)
- [The stdin and stdout contract](#the-stdin-and-stdout-contract)
- [Where hook text goes](#where-hook-text-goes)
- [Security model](#security-model)
- [Settings](#settings)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## Events

| Event | Fires | Can block |
| --- | --- | --- |
| `session_start` | the TUI starts, before the first frame | no |
| `session_end` | the TUI exits | no |
| `user_prompt_submit` | a prompt is submitted, before any model call | yes (deny) |
| `pre_tool` | before any tool call runs, after the permission rules | yes |
| `post_tool` | after a tool call returns successfully | no |
| `pre_edit` | before a `Write` or `Edit` runs, after `pre_tool` | yes |
| `post_edit` | after a `Write` or `Edit` succeeds, after `post_tool` | no |
| `stop` | a turn ends | no |
| `pre_compact` | before `/compact` or automatic compaction | no |
| `subagent_stop` | an explore subagent finishes | no |
| `notification` | Belai is waiting on you (see [notifications](notifications.md)) | no |

Tool hooks also see the tool calls explore subagents make. A subagent's own
prompt and turn end do not fire `user_prompt_submit` or `stop`; its end fires
`subagent_stop` instead.

## Hook files

Each hook is one JSON file in `~/.vulnetix/belai/hooks/` (or
`$BELAI_HOME/hooks/`). Enabled [plugins](plugins.md) add theirs, named
`plugin:name`. There is no project-level hooks directory: a repository never
supplies a command Belai will run.

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
| `name` | yes | unique name, shown in warnings and the trace |
| `event` | yes | one of the events above |
| `command` | yes | a path relative to the hooks directory, then arguments. No shell, no metacharacters, no `..` |
| `matcher` | no | `\|`-separated glob patterns over the tool name, case-insensitive (`Bash`, `Edit\|Write`, `mcp__*`). Ignored for events without a tool |
| `timeout_ms` | no | default 5000, at most 60000 |

Any other key fails validation, so a typo such as `matchr` cannot silently
widen a hook to every tool. An invalid file is skipped under the
`hook_invalid` posture gate. Hooks run in name order.

## The stdin and stdout contract

The hook receives one JSON object on stdin. Fields that do not apply to the
event are omitted.

```json
{
  "event": "pre_tool",
  "session_id": "…",
  "cwd": "/home/me/project",
  "tool_name": "Bash",
  "tool_input": {"command": "rm -rf build"},
  "tool_result_summary": "",
  "prompt": "",
  "subagent_id": "",
  "notification": ""
}
```

| Field | Set for |
| --- | --- |
| `event` | every event |
| `session_id` | every event: the transcript session id |
| `cwd` | every event: the session's current working directory |
| `tool_name`, `tool_input` | `pre_tool`, `post_tool`, `pre_edit`, `post_edit`: the call and its arguments |
| `tool_result_summary` | `post_tool`, `post_edit`: the first 2 KiB of the tool's output |
| `prompt` | `user_prompt_submit`: the prompt as typed |
| `subagent_id` | `subagent_stop`: the finished subagent |
| `notification` | `notification`: the event name, such as `permission` |

It may print one JSON object on stdout, or nothing:

```json
{"decision": "deny", "reason": "build/ is managed by make", "additional_context": ""}
```

- `decision` is `allow`, `deny` or `ask`, and is only read from the blocking
  events. Nothing, or `allow`, means no objection. `ask` is ignored for
  `user_prompt_submit`.
- `reason` explains a deny or an ask.
- `additional_context` is a short note for the model.

For a blocking event, a non-zero exit, a timeout, or stdout that is not a
single JSON object with a known decision counts as `deny`. For the other
events a failure is shown as a warning and otherwise ignored. When several
hooks answer, any `deny` wins over any `ask`, which wins over `allow`.

`reason` and `additional_context` are each capped at 2 KiB, and total output
at 64 KiB.

## Where hook text goes

- **A denied tool call** returns `tool result withheld: denied by hook
  "<name>"` to the model, followed by the hook's reason.
- **A denied prompt** ends the turn with `prompt blocked by hook "<name>"`
  and the reason, flattened to one line. It is shown to you and never sent to
  a model.
- **`additional_context` from tool hooks** is appended to that tool's result.
- **`additional_context` from `user_prompt_submit`** is appended to your
  prompt, marked as untrusted hook context, before the prompt classifier
  reads it.

## Security model

- A hook cannot widen permissions. It runs after the permission rules, so a
  `deny` rule wins before any hook runs. A hook's `allow` never skips an ask a
  rule or the mutating-tool default demanded. A hook's `ask` raises the
  permission ask even for a call a rule allowed; with the ask gate off it
  resolves like any other ask.
- Hook text reaching the model carries its own tool kind, `hook`, which is
  always sanitized and always classified. It classifies separately from the
  tool result it rides on, so it cannot get a clean result withheld and a
  clean result cannot vouch for it. The prompt-hook context is read by the
  prompt classifier along with the prompt. Hook text never enters the system
  block.
- Commands run without a shell, from their own directory, in their own
  process group, with the scrubbed environment used for Bash (no
  `*_API_KEY`, `*_TOKEN`, `*_SECRET`, `BELAI_*` from your shell) plus the
  identity variables `BELAI=1`, `BELAI_SESSION_ID` and `TRACEPARENT`. A
  command path that resolves outside the hooks directory, including through
  a symlink, is refused.
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

`enabled` defaults to `true`. The project layer may set `hooks.enabled` to `false`, never to `true`.

Each run is recorded in the `BELAI_TRACE` file under the `hook` phase with
its decision, the number of hooks that ran, and how many failed.

## Limitations

- Hooks are read when a session is built. A new file is picked up the next
  time the session is rebuilt (a new session, `/clear`, or a mode or model
  change).
- `session_start`, `session_end` and `pre_compact` from `/compact` fire from
  the TUI only; headless `-prompt` runs fire the tool, prompt, `stop` and
  automatic compaction events.
- `post_tool` does not fire for a call that failed to execute.

## Edge cases

- Several hooks for one event run one after another in name order. A deny
  from any of them wins over an ask, and an ask wins over an allow; a later
  hook still runs after an earlier one denied.
- A hook with no `matcher` sees every tool. A `matcher` is ignored for events
  that carry no tool (`stop`, `session_start`, …).
- `timeout_ms` of 0 means the 5-second default. A hook that runs longer is
  killed with its process group.
- `ask` from a hook, with the ask gate off, resolves to allow like any other
  ask. With no terminal to ask on, the `permission_ask_no_tty` posture
  decides, as it does for rules.
- `post_tool` and `post_edit` are not fired for a call that failed to run,
  was denied, or was withheld before it ran.
- A plugin's hooks run after yours, named `plugin:name`, and each resolves
  its command inside its own directory.
- `hooks.enabled: false` turns off every hook, the user's and every plugin's.
