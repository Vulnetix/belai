# Desktop notifications

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

Signet can tell you when it needs you: a permission ask is waiting, a long turn
finished, a plan is ready for review, or goal mode stopped. Useful when the
terminal is in another window or on another desktop.

- [Triggers](#triggers)
- [Backends](#backends)
- [Security model](#security-model)
- [Settings](#settings)
- [Limitations](#limitations)

## Triggers

| Event name | When |
| --- | --- |
| `permission` | a tool call is waiting on a permission ask |
| `clarify` | the agent asked you a clarifying question |
| `plan_ready` | a plan was written and waits for review |
| `turn_done` | a turn finished and took longer than `min_turn_seconds` |
| `goal_done` | goal mode completed |
| `goal_stalled` | goal mode stopped because work stopped advancing |
| `agent_done` | a background agent finished |

Every trigger also fires the `notification` [hook](hooks.md), so you can route
the same events somewhere else (a phone push service, a chat webhook) without
Signet knowing about it.

## Backends

| Backend | How |
| --- | --- |
| `osc` | OSC 9 and OSC 777 escape sequences written to the terminal. kitty, WezTerm, foot, iTerm2 and Windows Terminal turn these into system notifications |
| `bell` | the terminal bell (`BEL`) |
| `notify-send` | Linux desktop notification, fixed argv |
| `osascript` | macOS `display notification`, fixed argv |
| `auto` | `osc` when the terminal is known to support it, else `notify-send` or `osascript`, else `bell` |

## Security model

The notification text is written by the harness from a fixed set of
templates, for example `Signet: permission needed for Bash`. Model output,
tool output and file names never appear in it, so a notification cannot be
used to show you text a repository or web page chose. The external backends
run a fixed argv with the scrubbed environment.

## Settings

```json
{
  "notifications": {
    "enabled": false,
    "backend": "auto",
    "events": ["permission", "clarify", "plan_ready", "goal_done", "goal_stalled"],
    "min_turn_seconds": 30
  }
}
```

| Key | Default | Project layer |
| --- | --- | --- |
| `enabled` | `false` | dropped |
| `backend` | `auto` | dropped |
| `events` | as above | dropped |
| `min_turn_seconds` | `30` | dropped |

Notifications are a per-user preference, so the project layer cannot change
them. `/settings` has a `notifications` submenu with a test button.

## Limitations

- Signet cannot tell whether the terminal has focus in every terminal, so a
  notification may fire while you are looking at the window.
- Inside tmux, OSC sequences need `set -g allow-passthrough on`.
