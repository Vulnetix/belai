# Bash sandbox

**Status:** alpha-20260926. Shipped in an early form; the defaults may still
change.

Belai confines its own file tools to the workspace roots, but a command can
reach anything your user account can. The sandbox runs those commands under an
operating-system boundary: only the workspace roots, a private `/tmp` and the
usual tool caches are writable, Belai's own state directory is hidden, and
the network can be switched off.

- [What is confined](#what-is-confined)
- [Backends](#backends)
- [Settings](#settings)
- [When a command is blocked](#when-a-command-is-blocked)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## What is confined

It applies to:

- the `Bash` tool, in agent, plan, goal and code mode and in explore subagents, including a `Bash` call made from a [code-mode](code-mode.md) script
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
  `XDG_CACHE_HOME`, `CARGO_HOME` and `npm_config_cache` (a variable that is unset
  or holds a relative path adds nothing).
- Everything else is read-only.
- `~/.vulnetix/belai` (or `$BELAI_HOME`), which holds credentials and
  sessions, is hidden: the command sees an empty directory.
- The network is available unless `network` is `deny`.
- The command dies with Belai.

## Backends

| Platform | Backend | Notes |
| --- | --- | --- |
| Linux | `bwrap` (bubblewrap) | needs unprivileged user namespaces; Belai checks once per run that it works |
| Linux without a working `bwrap` | `landlock` (kernel LSM, 5.13+) | no namespaces: host `/tmp`, no pid namespace; network deny needs ABI 4 (Linux 6.7+); applied by the hidden helper `belai __landlock-exec` |
| macOS | `sandbox-exec` | generated profile |
| Windows, or Linux with neither | none | `auto` runs unsandboxed, `required` refuses |

`/sandbox` in the TUI shows whether commands are sandboxed here, the backend
(for Landlock, its ABI and whether it can cut the network off), the network
setting, every writable and hidden path, and the backend's limits.

### Keeping bubblewrap

bubblewrap gives the fuller sandbox (a private `/tmp`, a pid namespace, the
network off for every protocol), so where it only needs a switch, flip the
switch:

- Ubuntu 24.04 and later restrict unprivileged user namespaces with AppArmor.
  `sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0` lifts it for
  the session (`/etc/sysctl.d/` makes it stay), or give `bwrap` its own
  AppArmor profile with the `userns` permission.
- A Docker container needs a seccomp profile that allows `unshare` and
  `clone` with new namespaces: `--security-opt seccomp=unconfined`, or
  `--userns` remapping.

Otherwise Belai falls back to Landlock on its own, and `/sandbox` says so.

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
sandboxed `Bash` command exits non-zero, Belai adds a short note of its own
saying the command ran in the sandbox and what it could not do, so the model
asks you rather than retrying blindly.

## Limitations

- Files a command writes to `/tmp` are gone when it exits. Use a directory
  inside the workspace to pass files between commands.
- Your home directory stays readable (read-only) apart from Belai's state
  directory, so a command can still read files such as `~/.ssh/config`.
- With `network` set to `deny`, a supervised dev server is not reachable from
  your browser.
- MCP stdio servers run in the sandbox only when their settings say
  `sandbox: true` (see [MCP servers](mcp.md)).
- The native cloud tools (`AWS`, `Terraform`, `Kubectl` and the others) run outside
  the sandbox, as do `Vulnetix` scans. They hold only what their own sign-in
  gives them, plus, for `AWS` and `Terraform`, the temporary credentials of a role
  the profile declared or the user approved (see [AWS roles](agent-profiles.md#aws-roles)).
  Those credentials live in Belai's memory and reach only those two
  subprocesses. A command inside the sandbox cannot see them, and neither can
  `Env` or `Bash`.
- The microphone helper behind [voice input](voice.md) is started by Belai
  itself, outside the sandbox, and only from the voice engine. A command inside
  the sandbox sees a minimal device tree, so it cannot record audio.
- The sandbox is a boundary for the commands Belai runs. It is not a
  substitute for running Belai itself in a container.

Under Landlock, which has no mount or network namespaces:

- There is no private `/tmp`: commands share the host's, and what they write
  there stays.
- A hidden directory can be listed, though nothing in it can be read, written
  or run.
- `network: deny` cuts off TCP only (binding and connecting), from ABI 4
  (Linux 6.7+). UDP, so DNS, and unix sockets still pass; a supervised dev
  server cannot bind at all. On an older kernel the network stays open:
  `auto` runs and says so in `/sandbox`, `required` refuses.
- There is no pid namespace, so a command that would hold vault variables is
  refused rather than run where it could read another process's environment
  (see [vault variables](vault-env.md)).
- Writes under `/proc` are denied; bubblewrap's fresh `/proc` allowed a few.
- A directory on the way to a hidden path, or to a read-only path inside a
  writable one, takes no new entries at its top. A fleet worker's git common
  dir is read-only at its top, with the item's own ref, reflog and worktree
  directories writable beneath. Its commits stand, but each one ends with
  `error: Unable to create '.../packed-refs.lock': Permission denied`: git
  takes that lock to clear its cherry-pick and revert state, and a top the
  model could create and remove files in would also let it replace `config`
  and `HEAD`.
- A kernel at ABI 1 cannot move a file between directories inside the
  sandbox (that needs ABI 2).

## Edge cases

- A cache directory that does not exist is skipped; bubblewrap binds only
  what is there.
- A relative path in `extra_writable` is ignored.
- When `caches` is on, `TMPDIR` is writable if it points outside the private
  `/tmp` (the Pix sandbox image sets `/workspace/tmp`), so `go build`, `pip` and
  `npm` can make their work directories. A `TMPDIR` at or under `/tmp` is left
  alone.
- An unknown `mode` means `auto` when no layer sets a valid one. When layers
  are merged an unknown `mode` is skipped, so the layer below it stands: a
  project file saying `strict` over a global `required` leaves `required`. A
  `network` value other than `allow` means `deny`.
- Belai's state directory stays hidden even when it sits under a writable
  path.
- The backends are probed once per run, bubblewrap first. If it is installed
  but cannot start (unprivileged user namespaces off), the kernel is asked
  for Landlock; with neither, `auto` runs unsandboxed and `required` refuses.
  A probe that fails because the machine was out of processes or memory at
  that moment ("Resource temporarily unavailable", "Cannot allocate memory")
  is tried up to four times, 250 ms apart and doubling, before it counts; any
  other failure counts at once. A result reached through such a failure is
  not kept for the life of the process: after a five second cooldown the next
  command probes again, so a `belai rc` that was asked during one busy minute
  gets bubblewrap back once the machine is quiet. When no backend is found the
  refusal of an autonomous worker with Bash ends with why each probe failed,
  so a machine out of processes reads differently from one that cannot run
  bubblewrap.
- Only a sandboxed `Bash` command that exits non-zero gets the sandbox note;
  a successful one looks exactly as it would unsandboxed.
