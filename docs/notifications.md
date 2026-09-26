# Desktop notifications

**Status:** alpha-20260926. Shipped in an early form; the settings may still
change.

Signet can tell you when it needs you: a permission ask is waiting, a long turn
finished, a plan is ready for review, or goal mode ended. Useful when the
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
| `clarify` | the agent asked you a clarifying question or a mode choice |
| `plan_ready` | a plan was written and waits for review |
| `turn_done` | a non-goal turn finished and took at least `min_turn_seconds` |
| `goal_done` | goal mode (or an approved plan) completed |
| `goal_stalled` | goal mode stopped before completing |
| `agent_done` | a background agent finished |

Every trigger also fires the `notification` [hook](hooks.md) with the event
name in the `notification` field, whether or not desktop notifications are
on. That lets you route the same moments somewhere else (a phone push
service, a chat webhook) without Signet knowing about it.

## Backends

| Backend | How |
| --- | --- |
| `osc` | an escape sequence written to the terminal: OSC 777 for kitty, WezTerm, foot, rxvt and Konsole; OSC 9 for iTerm2, Windows Terminal, Ghostty and ConEmu. Inside tmux the sequence is wrapped for passthrough |
| `bell` | the terminal bell (`BEL`) |
| `notify-send` | Linux desktop notification, fixed argv |
| `osascript` | macOS `display notification`, fixed argv |
| `auto` | `osc` when the terminal is recognised, else `notify-send` or `osascript` when installed, else `bell` |

Escape sequences are written straight to `/dev/tty`, not through the screen
renderer, which strips OSC sequences from everything it draws.

## Security model

The notification text is written by the harness from a fixed set of
templates, for example `Permission needed for Bash`. The only variable part is
a tool or agent name, reduced to letters, digits and `_.:-` and capped at 48
characters. Model output, tool output and file names never appear, so a
notification cannot show you text a repository or web page chose. The
external backends run a fixed argv with the scrubbed environment and a
five-second timeout, off the UI goroutine.

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
| `backend` | `auto` (an unknown value also means `auto`) | dropped |
| `events` | as above; add `turn_done` or `agent_done` to opt in | dropped |
| `min_turn_seconds` | `30` | dropped |

Notifications are a per-user preference, so the whole key is ignored in a
project's `.vulnetix/settings.json`. Set it in your global `settings.json`.

## Limitations

- Signet cannot tell whether the terminal has focus, so a notification may
  fire while you are looking at the window.
- Inside tmux, OSC sequences need `set -g allow-passthrough on`.
- Headless `-prompt` runs do not notify.
