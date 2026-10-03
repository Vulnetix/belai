# Pix Sandbox

A Pix Sandbox is a machine Vulnetix runs for an account: one Cloudflare
container per purchase, running an unmodified `belai rc`. This page is the
contract between Belai and that machine. Belai has no code for it and does not
know it is hosted. Everything below is behaviour the released binary already has
when it is given a host id, a settings file, a few environment variables and one
`--dir` per repository.

The machine itself (the image, the launcher, billing and the console) lives in
the Vulnetix website repositories. Only what Belai does with it is stated here,
and each rule is pinned by `internal/rc/pixsandbox_test.go`.

## What the machine gives Belai

| Input | Value | Belai reads it as |
|---|---|---|
| `$BELAI_HOME/sync/host-id` | the sandbox's own uuid | the host id (`sessionsync.HostID`); a valid id already there is kept, never replaced |
| `$BELAI_HOME/settings.json` | sync on with remote prompts, `firewall.active` `vulnetix`, `classifier.provider` `typesafe` with `jev-latest` | ordinary global settings; guardrails stay at their default, on |
| `VULNETIX_ORG_ID`, `VULNETIX_API_KEY` | the account's org id and its ApiKey | the Vulnetix credential (`ApiKey <org>:<key>`) used for sync and the AI Firewall |
| `VULNETIX_API_TOKEN` | empty | no token login, which the website refuses for remote control |
| `TYPESAFE_API_KEY` | a placeholder, never the real key | the Jev key; the real one is swapped in at the network edge and never enters the machine |
| `--dir <path>` | one per cloned repository | the only offered and trusted directories |
| `--web-controls`, `--web-project-settings` | on by default, set per sandbox in the console's Launch tab | web sessions take session controls, and each offered directory's project preferences are managed from the console ([remote-control.md](remote-control.md#session-controls-from-the-web)) |
| `--web-allow-guardrails-off` | off by default; only with `--web-controls` | a web session may switch its guardrails off |

## Rules

- **The host id is canonical.** A uuid seeded into `sync/host-id` is the host id
  for the life of the machine. A file that is not a uuid is replaced by a fresh
  id, as on any install. The label a person gives the machine is display only on
  the website.
- **Trust is exactly the `--dir` list.** `belai rc` offers each `--dir` and the
  directories already trusted in the registry. The working directory only orders
  the list (`rc.Collect`), so a parent such as the folder the repositories are
  cloned into is never offered. With no directory at all, preflight fails and rc
  does not start, so a machine with no repository yet has no trusted directory.
- **Tools never see a credential.** `proc.ScrubbedEnv` removes every variable
  that ends in `_API_KEY`, `_TOKEN` or `_SECRET`, and everything that starts
  with `BELAI_`, from the environment of `Bash` and the other process tools. The
  ApiKey, the Jev placeholder and `BELAI_HOME` are all in that set, so a command
  a model runs cannot print them. The org id is not a secret and passes.
- **Jev is the classifier from the first call.** `classifier.provider`
  `typesafe` resolves to the fixed origin `https://api.typesafe.ai`, the model
  `jev-latest` and the key in `TYPESAFE_API_KEY`. With no key the lookup fails
  instead of sending an anonymous request, and a 429 from the service sends the
  classification to the agent model, so a daily cap degrades and never approves.
- **The AI Firewall is on.** `firewall.enabled` with the `vulnetix` instance is
  the active firewall, which routes model calls through the Vulnetix gateway
  with the same credential.
- **Web flags follow the image.** The launcher passes the web flags only to an
  image whose Belai takes them, so an older pinned image starts `belai rc`
  without them rather than with a flag it would refuse. A restart of `belai rc`
  uses the flags of the launch that was applied, never an unsaved draft.
- **Preflight passes with these inputs.** An ApiKey credential, sync on, remote
  prompts on, guardrails on and one directory is a passing preflight; a missing
  `vulnetix` CLI alone is a warning when the credential resolves anyway.

## Edge cases

- A token login (`VULNETIX_API_TOKEN` set) fails preflight. The machine sets it
  to the empty string so a stray token in its environment cannot shadow the
  ApiKey.
- A settings file that turns guardrails off fails preflight: `belai rc` never
  runs sessions nobody is watching without them.
- A directory inside a repository whose root is not trusted is skipped, not
  offered. The machine passes repository roots only.

See [Remote control](remote-control.md) for the daemon, [Session sync](session-sync.md)
for the mirror, [Firewall](firewall.md) for the gateway and [Jev jobs](jev-jobs.md)
for the classifier. The site section is described in [site.md](site.md).
