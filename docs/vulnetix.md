# `/vulnetix` — Vulnetix review + AI Firewall

`/vulnetix` is the Signet entry point for the Vulnetix CLI and the Vulnetix AI
Firewall. It runs Vulnetix review subcommands, surfaces CLI capability state,
keeps a history of projects found on this machine, and toggles the AI Firewall
for LLM traffic.

## Business rules

- **Never promote arbitrary repository bytes.** `vulnetix scan` output contains
  raw snippets and paths from the scanned repository. The runner composes only
  metadata (subcommand status, exit state, artifact counts) into the system
  transcript. Full artifact content is shown in the TUI but is never sent as a
  trusted system block without sanitisation and classification.
- **Subcommands are allowlisted.** `VulnetixSettings.Subcommands` is validated
  at save time and at run time against `AllowedSubcommands`. No user-supplied
  flags reach `exec.Command`.
- **Artifacts live under `.vulnetix/`, summary state under `.vulnetix/signet/`.**
  `code-review-summary.md` and `code-review-manifest.json` are written to
  `<workdir>/.vulnetix/signet/` so they stay outside `@file` admission and
  Vulnetix's own scans.
- **Signet's own files are excluded from the manifest.** The manifest uses
  `scanartifacts.Enumerate`, which classifies `settings.json`, `prompts.json`
  (a tombstone for the old library), `credentials.json`,
  `code-review-summary.md`, `code-review-manifest.json`, and anything under
  `signet/`, `plans/`, `goals/`, or `prompts/` as `KindSignet` and skips them.
- **Global cache, never inside the project.** `scanartifacts.Refresh` writes to
  `<GlobalDir>/scan-cache/<WorkdirKey>.json`. Invalidation is stat-only: a
  fingerprint over `(rel, size, mtime)` plus a schema version.
- **AI Firewall is fail-closed.** The firewall routes LLM traffic only when
  four conditions are true: (1) the user toggled it on via `/vulnetix firewall`,
  the `F10` key, or `SIGNET_FIREWALL=1`; (2) valid Vulnetix gateway credentials
  are present (`VULNETIX_API_KEY` + `VULNETIX_ORG_ID`, `VVD_ORG` + `VVD_SECRET`,
  or the logged-in Vulnetix CLI credential); (3) the provider maps to a
  gateway slug; and (4) resolving the credential succeeded without error. If any
  condition is missing, the toggle is stored but the run falls back to the
  native provider. The project-layer setting overrides the global value, and
  the CLI flag / environment variable overrides the project value.
- **Gateway routing uses the provider slug, not the URL.** `internal/aifirewall`
  maps providers (`openai`, `anthropic`, etc.) to gateway paths. The gateway
  base URL is `<gatewayHost>/<slug>/<org>/v1`. Anthropic chat uses
  `/v1/messages`; OpenAI-compatible surfaces use `/v1/chat/completions`. The
  gateway API key replaces the provider key.
- **`SIGNET_BASE_URL` wins.** If the user sets `SIGNET_BASE_URL`, it overrides
  the firewall gateway URL for that run. This lets tests and local gateways
  observe firewall-on traffic without hitting the production gateway.

## Command surface

| Input | Effect |
| --- | --- |
| `/vulnetix` | Run the configured review subcommands, then open the artifacts screen |
| `/vulnetix run` | Same as bare `/vulnetix` |
| `/vulnetix review` | Same as bare `/vulnetix` |
| `/vulnetix configure` | Open the CLI capability screen |
| `/vulnetix list` | Open the project history screen |
| `/vulnetix status` | Print CLI capabilities as plain text |
| `/vulnetix firewall` | Toggle the Vulnetix AI Firewall on/off |
| `/vulnetix help` | Show the available subcommands |

## Capability screen (`/vulnetix configure`)

Shows path, resolved symlinks, version, install method, update availability,
authentication state, plan, org ID, API reachability, and web URLs.

Keys: `r` re-probe, `l` history, `esc` back.

## History screen (`/vulnetix list`)

Lists projects from `internal/projectregistry`. Projects are merged from
session observations, completed reviews, manual pins, and an async TTL-gated
filesystem sweep (24 h by default). The sweep skips symlinks, dependency
directories (`node_modules`, `vendor`, ...), dot-directories, and system paths.

Keys: `↑↓` move, `/` filter, `enter` load artifacts, `r` re-sweep, `c`
configure, `esc` back.

## Artifacts screen (`/vulnetix artifacts`)

Lists classified artifacts with per-file counts. Superseded timestamp or branch
variants are marked but excluded from the active summary. The header shows the
union counts plus separate licence, suppressed, and risk-accepted tallies.

Keys: `↑↓` move, `t` start the built-in `signet:triage-vulns` agent for that
project, `l` history, `esc` back.

## AI Firewall (`/vulnetix firewall` and `F10`)

The Vulnetix AI Firewall routes LLM traffic from Signet through the Vulnetix
AI Firewall gateway. It can be toggled from anywhere with `F10` or with
`/vulnetix firewall` in chat. The footer shows a shield chip when the firewall
is on. The toggle is persisted in the active project's `settings.json`
(`vulnetix.firewall_enabled`) and merged with the global profile setting
(project overrides global; CLI flag overrides both).

When enabled, `run.Prepare` asks the credential resolver for the firewall
configuration. The resolver returns the gateway URL and Vulnetix API key;
`run.Config` then uses those as the provider base URL and API key. The model
catalog still fetches through the gateway when the user picks a provider.

Keys: `F10` toggle, `esc` or `/vulnetix firewall` to toggle.

## Runs panel

Every `/vulnetix` subcommand — and every CLI probe behind `configure` and
`status` — registers in the bottom runs panel (`f9`). The panel shows what argv
ran, live stdout/stderr, and exit state. `x` on a running or queued row kills
the whole process group; a killed subcommand stops the run so the remaining
subcommands never execute. `t` starts `signet:triage-vulns` on the selected
activity's project, keyed per project basename so two projects do not collide
on the instance name. `enter` round-trips the finished output to the model
exactly like a `!shell` result: it classifies first (unless guardrails are
off), seals as a shell attachment, and queues until the transcript is idle
when a turn is in flight.

The panel opens on the **activity** tab by default. `f8` opens it on the
**subagents** tab. It is bounded: it consumes at most one third of the terminal
height and refuses to open on terminals shorter than six rows so the chat
input remains usable.

## Severity parsing

CycloneDX `ratings[]` can mix CVSS, EPSS, SSVC, Coalition ESS, and licence
scales. Resolution order:

1. `vulnetix:max-severity` property (authoritative)
2. Maximum CVSS-scale score from sources named `NVD`, `GitHub`, `OSV`,
   `RedHat`, `NPM`, or `google_osi`, plus method `CVSSv*` (rounded to 1 dp)
3. `severity` word from CVSS-scale ratings
4. `SeverityUnknown` otherwise

EPSS/SSVC/Coalition ratings are ignored for the headline count. Licence-only
vulnerabilities (source `vulnetix-license-analyzer` or a
`vulnetix:license-severity` property) go to a separate licence bucket.

## SARIF severity precedence

1. `result.properties["security-severity"]`
2. `result.properties["severity"]` word (Vulnetix convention)
3. `result.level` (error→High, warning→Medium, note→Low, none→None)
4. Rule `defaultConfiguration.level`, then rule `security-severity`, then rule
   `severity`
5. Default `warning` (Medium) if nothing matches, counted as inferred

Suppressed results and `baselineState: "absent"` are excluded from headline
counts and counted separately.

## OpenVEX

Only `status: "affected"` counts. `fixed` and `not_affected` are excluded.
`under_investigation` is counted separately. `risk-accepted` entries are counted
separately, regardless of severity. Statements are deduplicated by
`(vulnerability.name, status, sorted products, action.status)`.
