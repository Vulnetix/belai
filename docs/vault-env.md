# Vault environment variables (Pix sandboxes)

Last updated: 2026-11-04

A Pix sandbox can have environment variables from the organisation's secrets vault
(the website's `/resolve/vault`). They are for the commands the agent runs, and
they are held so that the agent and the model never see a value.

Code: `internal/vaultenv` (store, scrubber, writer), `internal/sessionsync/vaultenv.go`
(the lease), hooks in `internal/tools/bash.go`, `internal/tools/vaultenv.go`,
`internal/tools/catalog.go`, `internal/tools/webfetch.go`, `internal/bgproc/manager.go`,
`internal/session`, and `internal/sandbox/sandbox.go`.

```mermaid
flowchart LR
    V[Vault in the cloud, KMS ciphertext] -- "GET /hosts/ID/vault/env, every 60 s, TLS" --> S[Session process, memory only]
    S -- "set on the child only" --> B[bubblewrap tool call]
    B -- "stdout, stderr" --> X[Scrubber]
    X -- "[vault:NAME]" --> M[Model, JSONL transcript, logs, background log]
```

## What it does

- **Lease.** Every session that syncs (`sessionsync.Syncer.Start`) asks the vault for
  its host's variables each minute. The answer is held in memory for a lease of up to
  15 minutes (`leaseSeconds`). If the vault cannot be reached the variables already
  held stay until their lease ends, then none are injected.
- **Expiry and revocation.** Each variable carries `expiresAt`. It is checked on every
  call, so an expired variable is not injected even before the next renewal. A variable
  the vault no longer grants is dropped at the next renewal.
- **Injection.** Added to the command's environment after the harness's credentials are
  removed, so only the process a command starts holds it. Belai's own environment never
  does. The removal is `proc.ScrubEnvKeeping` with the names the vault grants at that
  moment, so a variable the vault grants survives even when its name looks like a
  credential (`AWS_SESSION_TOKEN` ends in `_TOKEN`); the vault's value is added after and
  wins. One function builds this environment, `tools.VaultCommandEnv`, for full Bash, a
  model-started background command and the cloud native tools. The read-only Bash gate
  and the local native utilities (`Cat`, `Env`, `Git` and the rest) get none.
- **Native cloud tools.** `GH`, `AWS`, `Terraform`, `Glab`, `Tea`, `Kubectl` and the rest
  of the cloud catalogue run with the vault's variables after the scrubbed environment
  and before the profile's facts. The AWS role step runs last, so an assumed role's
  credentials still replace any AWS keys the vault granted (and the role is assumed from
  the vault's identity). Their output is scrubbed like Bash's.
- **Git credential helper.** When the vault holds `GH_TOKEN` or `GITHUB_TOKEN`, the same
  commands get `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_n` and `GIT_CONFIG_VALUE_n` that reset
  `credential.https://github.com.helper` and set it to `!gh auth git-credential`
  (`forge.RemoteAuthEnv`, the model-run form of `forge.RemoteAuth`). A model's
  `git clone` or `git push` of a private repository then authenticates through gh with the
  token already in its environment, instead of failing with `could not read Username`.
  The entries go after any `GIT_CONFIG_*` the environment already has, and the token is
  never a git argument or in a config file.
- **WebFetch.** A fetch of `https://api.github.com` sends the vault's GitHub token as
  `Authorization: Bearer`, so API reads are not anonymous ([Web fetch](web-fetch.md#github-api-authentication)).
- **Pid namespace.** While any variable is held, bubblewrap is started with
  `--unshare-pid`, so a command cannot read another process's `/proc/PID/environ`.
  Some containers mask paths under `/proc` (Cloudflare containers, so a Pix sandbox), and
  there bubblewrap cannot mount a fresh `/proc` in a new pid namespace ("Can't mount proc
  on /proc: Operation not permitted"). Belai probes this once. If the namespace cannot be
  had, it checks that a command inside the sandbox really cannot read the environment of a
  process outside it (the user namespace bubblewrap always creates normally already
  prevents that) and leaves the flag off only when the check shows it. When neither holds,
  the command is not run and the error says why; vault variables are never handed to a
  command that could read another process's environment. Under the Landlock backend,
  which has no pid namespace at all, such a command is refused outright.
- **Process hardening.** On the first lease with values the process is marked
  non-dumpable (`PR_SET_DUMPABLE 0`), so a same-user command cannot read its memory.
- **Names to the model.** The Bash tool description lists the variable names, never a
  value, and says to use `$NAME`.
- **Plain variables.** The vault's answer may mark a variable `"plain": true`: a region,
  a time zone, a proxy URL, anything that is not a secret. It is injected like any other
  but never scrubbed, so source text that holds the same string (`us-east-1`) reads as
  written instead of `[vault:AWS_DEFAULT_REGION]`. A plain value need not be 8
  characters, since nothing is matched against it. An older server omits the field, so
  every variable stays a secret there. A value this process already scrubbed as a secret
  stays scrubbed for the life of the process, even after the vault marks it plain.

## Scrubbing

`vaultenv.Store.Scrub` replaces a value with `[vault:NAME]`. It matches the value and
the forms it takes in output: base64 (standard and URL, padded and not), hex, URL
escapes, JSON escapes (with and without HTML escaping), a shell-quoted form, and each
line of a multi-line value. `vaultenv.Writer` catches a value split across writes by
holding back the last `longest form - 1` bytes.

It runs on:

| Where | Hook |
|-------|------|
| The tool result the model reads, and each streamed line | `tools.Bash.ExecuteStream` |
| A cloud native tool's result | `tools.Native.Execute` |
| A fetched page | `tools.WebFetch.Execute` |
| A background command's log file and live tail | `bgproc` (`vaultenv.NewWriter`) |
| Every session transcript line | `session.Store.AppendTo`, `Import`, `ForkAcross`, branch copy |
| This process's stdout and stderr, when they are not a terminal | `vaultenv.Protect` |

A secret value is at least 8 characters (`vaultenv.MinLen`); the vault refuses a shorter
one, because it could not be scrubbed without hiding ordinary words. A plain variable is
never scrubbed.

After a variable expires or is revoked it is no longer injected, but its value stays in
memory in the scrubber for the life of the process, so it is still hidden if it shows up
again in output.

## Reporting

Each renewal reports how many times each variable was scrubbed from output
(`POST /hosts/ID/vault/scrub`, names and counts only). The vault's Activity tab shows
them, so a value reaching an output is visible even though it was hidden.

## What it cannot do

Scrubbing hides a value in what Belai records. It cannot stop a command from
transforming a value (a hash, a reversal, a split into pieces) or sending it to a host
the sandbox can reach. Pair a sandbox that holds secrets with deny-by-default egress.
The vault page warns when a sandbox holds variables and allows all outbound traffic.
