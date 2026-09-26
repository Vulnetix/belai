# Bash sandbox

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

Signet confines its own file tools to the workspace roots, but a `Bash` command
can reach anything your user account can. The sandbox runs those commands
under an operating-system boundary: the rest of the filesystem is read-only
and the network is off unless you allow it.

- [What is confined](#what-is-confined)
- [Backends](#backends)
- [Settings](#settings)
- [When a command is blocked](#when-a-command-is-blocked)
- [Limitations](#limitations)

## What is confined

Inside the sandbox:

- The workspace roots (the working directory plus anything added with
  `/add-dir`) and a private temporary directory are writable.
- Everything else is read-only, and `~/.vulnetix/signet` (credentials, session
  store) is hidden.
- The network is unavailable when `sandbox.network` is `deny`.
- The process dies with Signet.

It applies to:

- the `Bash` tool
- inline `!cmd` from the prompt
- supervised processes and `ProcessRestart`
- stdio [MCP servers](mcp.md) that opt in

## Backends

| Platform | Backend | Notes |
| --- | --- | --- |
| Linux | `bwrap` (bubblewrap) | filesystem and network confinement |
| Linux, no `bwrap` | Landlock | filesystem only; network rule is reported as not enforced |
| macOS | `sandbox-exec` | generated profile |
| Windows | none | reported as unavailable |

## Settings

```json
{
  "sandbox": {
    "mode": "auto",
    "network": "deny",
    "extra_writable": []
  }
}
```

| Key | Values | Default | Project layer |
| --- | --- | --- | --- |
| `mode` | `off`, `auto`, `required` | `auto` | may raise (`off` to `auto` to `required`), never lower |
| `network` | `deny`, `allow` | `deny` | may set `deny` only |
| `extra_writable` | paths | `[]` | dropped |

- `auto` uses a backend when one is available and runs unsandboxed otherwise,
  with a footer chip saying so.
- `required` refuses to run a command when no backend is available.
- Turning guardrails off (`f3`, `/yolo`, `-guardrails=false`) turns the
  sandbox off with them.

The footer shows `sandbox: on`, `off` or `n/a`.

## When a command is blocked

A write outside the roots, or a network call with the network denied, fails
inside the command as a normal permission or resolution error. Signet adds a
short harness-written note to the result saying the sandbox was active, so the
model can ask you rather than retry blindly. You can allow the network for a
session from `/settings`.

## Limitations

- Package managers and build tools that write to a cache in your home
  directory (`~/.cache/go-build`, `~/.npm`) fail unless you add the cache to
  `extra_writable` in your global settings.
- Landlock needs Linux 5.13 or later.
- The sandbox is a boundary for the commands Signet runs. It is not a
  substitute for running Signet itself in a container.
