# Bash sandbox

**Status:** alpha-20260926. Shipped in an early form; the defaults may still
change.

Signet confines its own file tools to the workspace roots, but a command can
reach anything your user account can. The sandbox runs those commands under an
operating-system boundary: only the workspace roots, a private `/tmp` and the
usual tool caches are writable, Signet's own state directory is hidden, and
the network can be switched off.

- [What is confined](#what-is-confined)
- [Backends](#backends)
- [Settings](#settings)
- [When a command is blocked](#when-a-command-is-blocked)
- [Limitations](#limitations)

## What is confined

It applies to:

- the `Bash` tool, in agent, plan and goal mode and in explore subagents
- inline `!cmd` from the prompt
- supervised processes started from `/processes`

Inside the sandbox:

- The workspace roots (the working directory plus anything added with
  `/add-dir`) are writable, and so is a private `/tmp` that exists only for
  that command.
- With the default `caches: true`, the usual tool caches under your home
  directory stay writable so builds keep working: `~/.cache`, `~/go`, `~/.npm`,
  `~/.pnpm-store`, `~/.yarn`, `~/.bun`, `~/.deno`, `~/.cargo`, `~/.rustup`,
  `~/.m2`, `~/.gradle`, `~/.nuget`, `~/.gem`, `~/.dotnet`, `~/.pyenv` and a
  few more, plus the directories named by `GOCACHE`, `GOMODCACHE`, `GOPATH`,
  `XDG_CACHE_HOME`, `CARGO_HOME` and `npm_config_cache`.
- Everything else is read-only.
- `~/.vulnetix/signet` (or `$SIGNET_HOME`), which holds credentials and
  sessions, is hidden: the command sees an empty directory.
- The network is available unless `network` is `deny`.
- The command dies with Signet.

## Backends

| Platform | Backend | Notes |
| --- | --- | --- |
| Linux | `bwrap` (bubblewrap) | needs unprivileged user namespaces; Signet checks once per run that it works |
| macOS | `sandbox-exec` | generated profile |
| Windows, or Linux without a working `bwrap` | none | `auto` runs unsandboxed, `required` refuses |

`/sandbox` in the TUI shows whether commands are sandboxed here, the backend,
the network setting, and every writable and hidden path.

## Settings

```json
{
  "sandbox": {
    "mode": "auto",
    "network": "allow",
    "caches": true,
    "extra_writable": []
  }
}
```

| Key | Values | Default | Project layer |
| --- | --- | --- | --- |
| `mode` | `off`, `auto`, `required` | `auto` | may raise (`off` to `auto` to `required`), never lower |
| `network` | `allow`, `deny` | `allow` | may set `deny` only |
| `caches` | `true`, `false` | `true` | may set `false` only |
| `extra_writable` | absolute paths | `[]` | dropped |

- `auto` sandboxes when a backend works here and runs unsandboxed otherwise.
- `required` refuses to run a command when no backend works.
- For the strict policy, set `network` to `deny` and `caches` to `false`:
  only the roots and the private `/tmp` are writable, and nothing leaves the
  machine.
- Turning guardrails off (`f3`, `/yolo`, `-guardrails=false`) turns the
  sandbox off with them.

## When a command is blocked

A write outside the allowed paths, or a network call with the network denied,
fails inside the command as an ordinary permission or resolution error. When a
sandboxed `Bash` command exits non-zero, Signet adds a short note of its own
saying the command ran in the sandbox and what it could not do, so the model
asks you rather than retrying blindly.

## Limitations

- Files a command writes to `/tmp` are gone when it exits. Use a directory
  inside the workspace to pass files between commands.
- Your home directory stays readable (read-only) apart from Signet's state
  directory, so a command can still read files such as `~/.ssh/config`.
- With `network` set to `deny`, a supervised dev server is not reachable from
  your browser.
- MCP servers do not run in the sandbox yet.
- The sandbox is a boundary for the commands Signet runs. It is not a
  substitute for running Signet itself in a container.
