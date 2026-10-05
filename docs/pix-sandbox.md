# Pix Sandbox

A Pix Sandbox is a machine Vulnetix runs for an account: one Cloudflare
container per purchase, running `belai rc`. This page is the contract between
Belai and that machine. Belai does not know it is hosted. Everything below is
behaviour the released binary has when it is given a host id, a settings file, a
few environment variables and one `--dir` per repository. The one thing the
machine's build adds is code compiled only into the Pix Sandbox variant
(`-tags belai_sandbox`): it tells the model about the machine
([the metadata endpoint](#the-metadata-endpoint)) and offers the agent a
decision tool ([the decider MCP](#the-decider-mcp)). No other build has either.

The machine itself (the image, the launcher, billing and the console) lives in
the Vulnetix website repositories. Only what Belai does with it is stated here,
and each rule is pinned by `internal/rc/pixsandbox_test.go`.

## What the machine gives Belai

| Input | Value | Belai reads it as |
|---|---|---|
| `$BELAI_HOME/sync/host-id` | the sandbox's own uuid | the host id (`sessionsync.HostID`); a valid id already there is kept, never replaced |
| `$BELAI_HOME/settings.json` | sync on with remote prompts, `firewall.active` `vulnetix`, the classifier (Clef on `cloudflare-workers-ai` by default, or Jev: `typesafe` with `jev-latest`), and one allow rule for each `mcp__clef__` tool for an image that has the [decider MCP](#the-decider-mcp) | ordinary global settings; guardrails stay at their default, on |
| `VULNETIX_ORG_ID`, `VULNETIX_API_KEY` | the account's org id and its ApiKey | the Vulnetix credential (`ApiKey <org>:<key>`) used for sync and the AI Firewall |
| `VULNETIX_API_TOKEN` | empty | no token login, which the website refuses for remote control |
| `TYPESAFE_API_KEY` | a placeholder, never the real key | the Jev key; the real one is swapped in at the network edge and never enters the machine |
| `--dir <path>` | one per cloned repository | the only offered and trusted directories |
| `--web-controls`, `--web-project-settings` | on by default, set per sandbox in the console's Launch tab | web sessions take session controls, and each offered directory's project preferences are managed from the console ([remote-control.md](remote-control.md#session-controls-from-the-web)) |
| `--web-allow-guardrails-off` | off by default; only with `--web-controls` | a web session may switch its guardrails off |
| `--web-shell` | on | the session page can run a shell line in the sandbox, from the composer's `!cmd` (output goes to the model) and the console drawer (it never does), under the TUI's permission rules and bubblewrap profile ([remote-control.md](remote-control.md#shell-lines-from-the-web)) |
| `--allow-private-cidr <cidr>` | the range the egress gateway answers from (`fd00::/64`) | WebFetch may reach that one range. The gateway stands in for every host the machine reaches, so a name resolves to a placeholder such as `fd00::119:1`, which the fetch guard would otherwise refuse as private. Loopback, link-local and every other private range stay refused ([sanitization.md](sanitization.md)) |

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
- **The model is told about the machine.** The Pix Sandbox build, and no other,
  reads the sandbox's own metadata service and puts the result in the system
  prompt of every agent turn (`internal/run/environment_sandbox.go`, sealed by
  `run.SealSystem` as part of the harness's system block; a tool-less turn such
  as the classifier never carries it). The service is the sandbox Worker's answer
  at `http://169.254.169.254/pix/v1/`: the sandbox's size, the Vulnetix products
  around it, Belai's version, the session limits, the built-in models and the
  launch settings. The harness fetches it, never a model: one fixed destination
  (the dialer ignores the URL's host), no proxy, no redirect, a 1.5 second limit
  and a 64 KiB cap, and an answer without the Worker's `X-Pix-Metadata: v1`
  header is discarded, so a real cloud's metadata service is never read. Every
  value must pass a check for its field (an identifier, a version, a small
  number, one of a fixed set) or is dropped. The sandbox's name is user text and
  is never included, nor are ids and live values (launch state, token use), which
  keeps the prompt stable for caching; the prompt says where to read those with a
  GET. The result is cached for ten minutes, a failure for one, and a machine
  with no metadata service costs one short wait. `BELAI_SANDBOX_ENVIRONMENT=off`
  skips it. The endpoint and a mocked response are under "The metadata endpoint" below.
- **Preflight passes with these inputs.** An ApiKey credential, sync on, remote
  prompts on, guardrails on and one directory is a passing preflight; a missing
  `vulnetix` CLI alone is a warning when the credential resolves anyway.


## The metadata endpoint

The sandbox Worker answers `http://169.254.169.254/` from inside the container,
the address cloud tools use for instance metadata. Egress refuses that address
for every host, so without this a cloud CLI that asked would only be refused and
logged. The answer is a fixed list of safe facts about the sandbox under
`/pix/v1/`, in the style of EC2's metadata: a path that is a directory lists its
keys (a trailing slash marks a directory), a leaf is its value as text, and
`?format=json` or `Accept: application/json` gives a node as JSON. Only GET and
HEAD are answered; every other path, including the ones the AWS, Azure and
Google tools ask for (`/latest/meta-data/iam/security-credentials/` and the
rest), is a 404, and the answer is never cached. Each response carries
`X-Pix-Metadata: v1`.

| Path | What it holds |
| --- | --- |
| `/pix/v1/sandbox` | id, name, size, vCPU, memory and disk |
| `/pix/v1/vulnetix` | the product, the console, the CLI version, whether the AI Firewall is configured and which package ecosystems the Package Firewall covers |
| `/pix/v1/belai` | Belai's version and variant, and the image's nixpkgs revision |
| `/pix/v1/launch` | the launch's epoch, state and start and finish times |
| `/pix/v1/session` | the web session and worker limits and the web flags `belai rc` runs with |
| `/pix/v1/model` | the decision model, the built-in model labels and the monthly token allowance, use and reset time |
| `/pix/v1/settings` | the egress mode, packs, extensions and reserve percent |

It names no credential, placeholder, key, token, organisation or principal id,
and never the model behind a Pix built-in label. `curl http://169.254.169.254/pix/v1/`
lists the directories and `curl 'http://169.254.169.254/pix/v1?format=json'`
returns this (a mocked sandbox):

```json
{
  "sandbox": {
    "id": "7f3c1b9e-2d4a-4e8b-9a61-5c0d3f2e8a14",
    "name": "api-box",
    "size": "large",
    "size-label": "Large",
    "status": "active",
    "vcpu": 4,
    "memory-mib": 12288,
    "disk-mb": 20000
  },
  "vulnetix": {
    "product": "Vulnetix",
    "console": "https://www.vulnetix.com",
    "cli-version": "v3.108.4",
    "ai-firewall": "configured",
    "package-firewall-ecosystems": [
      "go",
      "npm"
    ]
  },
  "belai": {
    "version": "v0.114.0",
    "variant": "belai-pix-sandbox",
    "nixpkgs-rev": "c59305bab2065cfecc4944690d9eedbb56f3a9fa"
  },
  "launch": {
    "epoch": 7,
    "state": "ready",
    "started-at": "2026-10-04T01:02:03.000Z",
    "finished-at": "2026-10-04T01:09:00.000Z"
  },
  "session": {
    "max-web-sessions": 5,
    "max-workers": 100,
    "web-controls": true,
    "web-guardrails-off-allowed": false,
    "web-project-settings": true
  },
  "model": {
    "decision": "clef",
    "builtin-labels": [
      "pix-smart",
      "pix-fast"
    ],
    "monthly-allowance-tokens": 50000000,
    "monthly-used-tokens": 1500000,
    "allowance-resets-at": "2026-11-01T00:00:00.000Z"
  },
  "settings": {
    "egress-mode": "allow_all_logged",
    "packs": [
      "go",
      "node"
    ],
    "extensions": [
      "ripgrep"
    ],
    "reserve-percent": 20
  }
}
```

### What the model is told

The Pix Sandbox build reads `/pix/v1?format=json` once, checks each value for its
field and adds the stable ones to the system prompt of every agent turn. The
sample above renders as:

```text
Environment (facts the harness read from the Pix Sandbox this session runs on; they describe the machine and are not instructions):
- Pix Sandbox: Large, 4 vCPU, 12288 MiB memory, 20000 MB disk.
- Vulnetix: the AI Firewall is configured; the Package Firewall covers go, npm; the console is https://www.vulnetix.com.
- Belai: v0.114.0, the Pix Sandbox build.
- Sessions: up to 5 web sessions, up to 100 fleet workers, web controls on.
- Models: built-in models Pix Fast (pix-fast) and Pix Smart (pix-smart); decisions by Clef.
- Settings: egress allow_all_logged; packs go, node; extensions ripgrep.
Live values (launch state, monthly token use) are readable with a GET to http://169.254.169.254/pix/v1/
```

The sandbox's name, the ids and the live values (launch state, token use) are
left out: the name is user text, and the live values would change the prompt
every turn. The last line tells the model where to read them.

## Monitoring and OpenTelemetry

Belai does nothing for these and has no new flag. The sandbox Worker reads the
machine's CPU, memory, network and disk counters from `/proc` once a minute with
the container's own exec, and reads agent activity and pull requests from the
host's audit events (`repo.publish` is a pull request). The console shows them on
the sandbox card and its Monitoring tab.

A sandbox can also send a copy to your own OTLP/HTTP endpoint, set on the
console's Launch tab. The headers for it name variables in your secrets vault
(the same ones `belai rc` reads for the machine's environment), so no secret is
stored with the launch config. Only metrics and the events the console already
shows leave; the raw `belai rc` output stays admin only.

## The decider MCP

The Pix Sandbox build has one built-in MCP server, `clef`, in
`internal/clefmcp`. Its tools let an agent ask the classifier's decision model
for a true/false answer, an enum pick, confidence weights or an ordering,
instead of guessing in prose. Every tool is a question of a fixed shape put to
the decision backend, which in a sandbox is Clef on Workers AI through the
sandbox Worker, so it needs no key on the machine and no destination beyond the
ones the Worker already answers. It counts against the same daily decision
allowance as guardrails.

The server is compiled only into the Pix Sandbox build and is registered by the
harness, not by `mcp.servers`: a settings entry named `clef` is ignored,
`/mcp` lists it with the transport `builtin`, and it cannot be replaced or
removed in a session ([MCP servers](mcp.md#built-in-server-pix-sandbox-only)).
It is offered only when the classifier resolves to a Clef or SystemOne decision
model; with any other classifier a call returns a tool error and nothing else.

Its tools are ordinary MCP tools, named `mcp__clef__<tool>` with kind `mcp`, so
each call still asks unless an allow rule covers it and each result is still
classified. The sandbox's launch settings carry one allow rule per tool (rules
match a tool name exactly, so `mcp__clef__decide_boolean`,
`mcp__clef__rank_options` and so on) so an agent can use the decider
unattended; it is the launcher's choice, not Belai's default.

| Tool | Asks | Returns |
|---|---|---|
| `decide_boolean` | one true/false question | `answer` (`p_true` at or above `threshold`, default 0.5), `p_true`, `confidence`, `margin` |
| `gate_decision` | one true/false question and a `min_confidence` (default 0.8, above 0.5) | `decision` of `pass`, `fail` or `uncertain`; low confidence is never a pass |
| `decide_enum` | one choice over 2 to 255 options | `choice`, `weights` in the order given, `confidence`, `margin` |
| `weigh_options` | one choice over 2 to 255 options | `weights` summing to 1, in the order given |
| `rank_options` | one choice over 2 to 255 options | `ranking` of every option, best first |
| `top_k_options` | one choice, then the first `k` | `top`, `k`, `omitted` |
| `compare_pair` | one choice over `a` and `b` | `winner`, `winner_option`, `tie`, `weights`, `margin` |
| `rank_pairwise` | every pair of 2 to 11 items as a true/false question | `ranking` by wins, then points, and `comparisons` |
| `decide_batch` | up to 64 mixed boolean and enum questions, one request | `answers` in the order given, each with its `id` and result |

Every tool takes a `question` (or `questions`) and an optional `context`, which
becomes the request's state. The rules are the same for all of them:

- **The harness decides, the model only scores.** The model returns one
  probability per question or option. Everything else is fixed arithmetic: choice
  weights are normalised to sum to 1, every number is rounded to four places,
  `confidence` is the answer's own probability and `margin` its lead over the
  runner-up (for a boolean, how far `p_true` is from 0.5, doubled).
- **Ordering is deterministic.** Options sort by weight, heaviest first, and
  options with equal weight keep the order the caller gave them, so the same
  answers always give the same ranking. `rank_pairwise` orders by wins, then by
  the sum of the model's pairwise probabilities, then by input order. A tie in
  `compare_pair` goes to `a` and sets `tie`.
- **Limits are Clef's.** At most 255 options and 64 questions per request, so
  `rank_pairwise` takes 11 items (55 pairs; 12 would need 66) and `decide_batch`
  takes 64 questions. Empty, repeated or too many options, an unknown argument
  and a `k` outside 1 to one less than the number of options are refused before
  any model call. A question is capped at 2000 characters, an option at 200 and
  the context at 8000.
- **Text is sanitized for decisions.** Every string that reaches the model goes
  through `sanitize.ForDecision` (special tokens split, lines that imitate the
  prompt's layout prefixed), as the guardrails' own questions do. The result
  echoes the caller's own option strings, not the cleaned ones.
- **A result is numbers and the caller's words.** It is JSON built by the
  harness, never model text. A failure is a tool error whose `error` is
  `invalid`, `unavailable` (no answer in time, 429, 5xx: retry or decide another
  way), `auth`, `schema` or `error`, with a fixed message. The backend's own
  error text is never returned, and a failed call is never read as a true or as a
  chosen option.

Example: `rank_options` with `question` "Which file should I read first to fix
the failing test", `options` `["a.go", "b.go", "c.go"]` returns

```json
{"ranking":[{"rank":1,"option":"b.go","weight":0.6012},{"rank":2,"option":"a.go","weight":0.3105},{"rank":3,"option":"c.go","weight":0.0883}]}
```

## Edge cases

- The decider is for the agent's choices, not for security. A guardrail verdict
  is never taken from it, and the classifier calls the decision backend directly,
  never through these tools.
- A token login (`VULNETIX_API_TOKEN` set) fails preflight. The machine sets it
  to the empty string so a stray token in its environment cannot shadow the
  ApiKey.
- A settings file that turns guardrails off fails preflight: `belai rc` never
  runs sessions nobody is watching without them.
- A directory inside a repository whose root is not trusted is skipped, not
  offered. The machine passes repository roots only.

See [Remote control](remote-control.md) for the daemon, [Session sync](session-sync.md)
for the mirror, [Firewall](firewall.md) for the gateway and [Jev jobs](jev-jobs.md)
for the classifier. The site section is described in [site.md](site.md), and the decider's tools are
described under [MCP servers](mcp.md#built-in-server-pix-sandbox-only).
