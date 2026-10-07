# Hooks

**Status:** alpha-20260926. Shipped in an early form; the contract may still
change.

Hooks run a command of yours at fixed points in a session: before and after a
tool call, when you submit a prompt, when a turn ends, before compaction, and
when Belai needs your attention. A hook can log, lint, veto a tool call, or
hand the model a short note.

- [Events](#events)
- [Hook files](#hook-files)
- [Hook bundles](#hook-bundles)
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

## Hook bundles

A bundle is the form the Claude Code hooks dialect arrives in, the one Codex and
the Vulnetix CLI's `agent install` also write: a definition and the script
files its commands run, in one directory,
`~/.vulnetix/belai/hooks/<name>/`. The library's `hook` items install as
bundles ([library items](library-items.md)), and you can write one by hand.

```json
{
  "name": "pix",
  "description": "Dependency and change guards",
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash|Edit|Write", "hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}
    ]
  }
}
```

That is `hooks.json` in the bundle's directory. Keys starting with `_` (a
comment block) are ignored, any other unknown key rejects the file. Belai's own
flat hook files keep working beside it.

| Dialect | Belai |
| --- | --- |
| `PreToolUse`, `PostToolUse` | `pre_tool`, `post_tool` (a matcher of `Edit\|Write` narrows them to edits) |
| `UserPromptSubmit`, `Stop`, `SubagentStop` | `user_prompt_submit`, `stop`, `subagent_stop` |
| `SessionStart`, `SessionEnd`, `PreCompact`, `Notification` | `session_start`, `session_end`, `pre_compact`, `notification` |
| any other event, or a handler whose `type` is not `command` | skipped, with a note |

A matcher is read as Belai's glob list: `*` or nothing matches every tool, and
`A|B`, `^A$` and `mcp__server__.*` translate exactly. A matcher that uses any
other regular expression syntax rejects the bundle, so a hook never fires on
fewer calls than it was written for. `timeout` is in seconds (default 30, at most
60). Hooks are named `bundle:<name>:<Event>:<group>-<handler>` and run in that
order.

**What a command may run.** The command is split once into words, with no shell:
a word is plain text (letters, digits and `_ . / : = @ % + , -`, or a quoted
phrase of them), and `$VAR`, a redirect, a pipe, a glob or a `~` rejects the
bundle. `${CLAUDE_PLUGIN_ROOT}` is the bundle's own directory. The first word is
either

- a file the bundle carries (a regular file inside the bundle, resolved after
  symlinks, never an absolute path or `..`), or
- a bare program name you listed in [`hooks.allowed_programs`](#settings), found on
  `PATH` each time it runs and never inside the bundle.

Every other word is refused if it is absolute, starts with `~`, has a `..`
segment or resolves outside the bundle; an allowed program is also refused an
argument that hands it code to run (`-c`, `-e`, `--eval`). One command that fails
rejects the whole bundle (a warning names the reason, such as a program that is
not in `allowed_programs`).

**The protocol.** The hook gets the dialect's payload on stdin: `session_id`,
`transcript_path` (empty), `cwd`, `hook_event_name` (`PreToolUse`),
`permission_mode` (`default`), `tool_name`, `tool_input`, `tool_use_id`,
`tool_response` (`{"summary": …}`, the first 2 KiB, not the whole output),
`prompt`. `CLAUDE_PROJECT_DIR` (the session's directory) and
`CLAUDE_PLUGIN_ROOT` (the bundle) are added to the scrubbed environment. Its
answer maps onto the same three decisions:

| The hook | Belai |
| --- | --- |
| exits 2 (stderr is the reason) | deny, on a blocking event |
| `hookSpecificOutput.permissionDecision` `allow`, `deny` or `ask`, with `permissionDecisionReason` | that decision |
| `decision` `block` or `approve`; `continue: false` | deny; no objection; deny |
| `additionalContext`, or plain text on `UserPromptSubmit` and `SessionStart` | a note for the model |
| `updatedInput`, `systemMessage` | ignored: a hook can narrow a call, never rewrite it |
| exits 1 or any other non-zero, a timeout, a non-JSON answer to a tool event | deny on a blocking event, a warning otherwise |

Bundle hooks are held to the same rules as any hook: they run after the
permission rules and only narrow, their text is classified as hook text, and a
blocking hook that fails denies (stricter than the dialect, where exit 1 does not
block).

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
  "tool_use_id": "",
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
| `tool_use_id` | the tool events: the call's id |
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

`reason` and `additional_context` are each capped at 2 KiB. Belai reads at most
64 KiB of a hook's stdout, and separately of its stderr, and discards the rest.
On a blocking event, a decision whose JSON is cut off by that cap cannot be
parsed and counts as `deny`.

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

`allowed_programs` lists the bare names of programs (for example `["vulnetix"]`)
a [bundle](#hook-bundles)'s commands may run from `PATH`; the default is none, so a
bundle runs only the files it carries. It is read from your own settings only: a
project layer's value is ignored with a note.

Each run is recorded in the `BELAI_TRACE` file under the `hook` phase with
its decision, the number of hooks that ran, and how many failed.

## Limitations

- Hooks are read when a session is built. A new file is picked up the next
  time the session is rebuilt (a new session, `/clear`, or a mode or model
  change).
- `session_start`, `session_end`, `notification` and `pre_compact` from `/compact` fire from
  the TUI only, so a bundle's `SessionStart` hook does nothing under `belai rc`, headless runs or a Pix Sandbox; headless `-prompt` runs fire the tool, prompt, `stop` and
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
- A bundle's hooks run after the flat files and before a plugin's.
- A plugin's hooks run after yours, named `plugin:name`, and each resolves
  its command inside its own directory.
- `hooks.enabled: false` turns off every hook, the user's and every plugin's.
