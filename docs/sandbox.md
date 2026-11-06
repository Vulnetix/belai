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
- [Egress proxy](#egress-proxy)
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
- The network is available unless `network` is `deny`, or `proxy`, which leaves
  only the [egress proxy](#egress-proxy).
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
    "extra_writable": [],
    "egress_allow": []
  }
}
```

| Key | Values | Default | Project layer |
| --- | --- | --- | --- |
| `mode` | `off`, `auto`, `required` | `auto` | may raise (`off` to `auto` to `required`), never lower |
| `network` | `allow`, `proxy`, `deny` | `allow` | may move toward `deny` only (`allow` to `proxy` to `deny`) |
| `caches` | `true`, `false` | `true` | may set `false` only |
| `extra_writable` | absolute paths | `[]` | dropped |
| `egress_allow` | host names and `*.domain` wildcards | `[]` | dropped |

- `auto` sandboxes when a backend works here and runs unsandboxed otherwise.
- `required` refuses to run a command when no backend works.
- `proxy` is the middle setting: a command has no network of its own and reaches
  only the hosts in `egress_allow`, through a proxy Belai runs. See
  [Egress proxy](#egress-proxy).
- For the strict policy, set `network` to `deny` and `caches` to `false`:
  only the roots and the private `/tmp` are writable, and nothing leaves the
  machine.
- Turning guardrails off (`f3`, `/yolo`, `-guardrails=false`) turns the
  sandbox off with them. The [hazard rails](hazard-rails.md) still ask: they
  are a floor above the rules and the sandbox, and an approved command runs
  inside the sandbox.

## Egress proxy

**Status:** alpha-20261106.

`sandbox.network: "proxy"` lets a sandboxed command reach the hosts you named
and nothing else, so `npm install`, `go mod download` or `pip install` work
while a command cannot send your workspace to a server of its choosing. Belai
decides, by rule. No model is asked whether a host is safe.

```json
{
  "sandbox": {
    "network": "proxy",
    "egress_allow": ["registry.npmjs.org", "proxy.golang.org", "*.githubusercontent.com"]
  }
}
```

An entry is a host name, or `*.` and a domain of at least two labels. A wildcard
covers every name beneath the domain at any depth and never the domain itself:
write `example.com` and `*.example.com` to allow both. An entry is not an
address, an internal name (`localhost`, `*.local`, `*.internal`, a single label),
a bare `*` or a wildcard over a top-level domain (`*.com`); a malformed entry is
dropped with the rest of the list kept. There is no public suffix list, so
`*.co.uk` is accepted: you chose it. At most 256 entries. The list is empty by
default, so `proxy` alone reaches nothing.

### How it works

- Belai starts one filtering forward proxy per process and allowlist. It listens
  on a unix socket in `~/.vulnetix/belai/egress` (mode 0700, socket 0600),
  removed when Belai exits; a socket left by a crashed run is swept at the next
  start.
- The command keeps no network of its own. Under bubblewrap it stays in a new
  network namespace (`--unshare-net`). Under Landlock, from ABI 4, TCP connects
  are allowed to one loopback port and nowhere else. Under `sandbox-exec` the
  profile allows outbound to `localhost:PORT` only.
- A small relay (`belai __egress-exec`, the second hidden helper beside
  `__landlock-exec`) runs beside the command. It listens on `127.0.0.1` and
  copies bytes to the proxy's socket. Under bubblewrap the state directory is
  still hidden: only that one socket is bound back in.
- `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (and their lower case forms) point at
  the relay and `NO_PROXY` is emptied, so `curl`, `git`, `npm`, `pip`, `go` and
  most clients use it without configuration. A client that ignores them finds
  no network.
- The proxy answers `CONNECT host:port` and plain `http://` requests in absolute
  form. It does not decrypt TLS: it sees the host a client named, nothing of
  the traffic. A plain request is forwarded with the checked `Host`, one request
  to a connection (`Connection: close`).
- For each request it checks the host (`netguard.CheckHostPort`: canonical, not
  an address in a forbidden range, not an internal name), the port (80 or 443
  only), and the allowlist (`netguard.MatchHost`). It then resolves the name
  itself, refuses the connection if any address is not public
  (`netguard.Forbidden`), and dials one of those exact addresses, so a DNS
  answer cannot change between the check and the connection. A host that resolves
  to both a public and a private address is refused.
- Limits: 64 connections at once, 8 KiB of request line and headers, a connect
  timeout of 10 seconds and a 60 second idle timeout.
- A refusal is a `403` with fixed text that names no host. The refusal is also
  appended to the `Bash` result (`the egress proxy refused: ...`), with the host
  cleaned to one line, so the model asks you instead of looking for a way around.
  That note is part of a `Bash` result, which is classified like any command
  output.

### Allowing a host

`/egress` shows the mode, the allowlist from settings, the hosts you allowed for
this session and the most recent refusals. `/egress allow HOST` adds a host (or
`*.domain`) for the rest of the session and `/egress revoke HOST` removes it; both
validate the entry like a settings entry. Run the command again afterwards.
Only a person typing in the TUI can do this: there is no tool for it, a project
file cannot do it, and headless, ACP and `belai rc` sessions use the settings
list as it stands and refuse every other host. To keep a host, add it to
`sandbox.egress_allow` in your own settings file. `/sandbox` shows the network
line.

### Failure is closed

- A project file may move `network` from `allow` to `proxy` to `deny`, never back,
  and can neither add to nor replace `egress_allow`. A `network` value that is
  not `allow`, `proxy` or `deny` means `deny`.
- With `mode: required`, a proxy that cannot be set up (the socket path is too long
  for a unix socket, the state directory is not a plain directory) or a backend
  that cannot confine a command to it (Landlock before ABI 4, which cannot
  restrict TCP) refuses the command with an error that says why.
- With `mode: auto` the same situations run the command as `network: deny` does
  there, never with the network open because the proxy was missing. Under
  Landlock before ABI 4 that is not enforced either (see
  [Limitations](#limitations)), and `/sandbox` says so.
- With the sandbox off (`mode: off`, or guardrails off) nothing is confined and the
  proxy is not used.
- The proxy serves every caller of the sandbox: `Bash`, inline `!cmd`,
  supervised and background processes, the post-end test pass, fleet quality and
  gate runs, worktree setup and teardown, and MCP servers that opt in. A
  worktree hook run with `worktrees.network: deny` still gets no network.

### What is recorded

Refused hosts stay in a bounded in-memory list (the last 64) that `/egress` reads.
Nothing about a host is written to disk or to the audit log. The audit log gets
an `egress.denied` event for the first refusal of each reason, then at the 10th,
100th and every 1000th, carrying the reason (`not_allowed`, `port`, `invalid`,
`address`, `resolve`, `limit`) and a running `count`. See [Audit log](audit.md).

### Limits of the proxy

- Without TLS interception the proxy cannot tell what is sent to an allowed host.
  An allowed host that accepts uploads can still receive data, and a wildcard
  covers every subdomain, including ones other people control (for example on a
  hosting service). Allow the narrowest names that work.
- It filters by the name a client asks for. A client that connects to an allowed
  name and sends a different `Host` header over a tunnel is not inspected.
- TCP on ports 80 and 443 only: no `ssh`, `git://` or database traffic.
- Under Landlock, UDP and unix sockets still pass, as with `network: deny`.

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
- With `network` set to `deny` or `proxy`, a supervised dev server is not reachable
  from your browser.
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
  dir is read-only, with the item's own ref and reflog directories writable
  beneath. The worker's git runs on a private copy of the admin and common
  directories (see [fleet](fleet.md#git-in-the-worktree)), so the lock git
  takes on `packed-refs` at the end of every commit (it deletes `AUTO_MERGE`
  through a ref transaction, and git 2.56 locks `packed-refs` for any
  deletion; git's development branch no longer does for such refs) is taken in a directory the worker may write, and a commit prints
  nothing.
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
  `network` value other than `allow` or `proxy` means `deny`.
- Belai's state directory stays hidden even when it sits under a writable
  path.
- The backends are probed once per run, bubblewrap first. If it is installed
  but cannot start (unprivileged user namespaces off), the kernel is asked
  for Landlock; with neither, `auto` runs unsandboxed and `required` refuses.
  A probe that fails because the machine was out of processes or memory at
  that moment ("Resource temporarily unavailable", "Cannot allocate memory",
  or the Go runtime's "failed to create new OS thread", which is how the Landlock
  helper dies when no thread is left)
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
