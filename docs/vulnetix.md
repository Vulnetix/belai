# `/vulnetix` — Vulnetix configure + scan-history screens

`/vulnetix` is the Signet entry point for the Vulnetix CLI. It runs scans,
displays the local CLI capability state, and keeps a history of the projects
found on this machine.

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

## Command surface

| Input | Effect |
| --- | --- |
| `/vulnetix` | Run the configured subcommands, then open the artifacts screen |
| `/vulnetix run` | Same as bare `/vulnetix` |
| `/vulnetix configure` | Open the CLI capability screen |
| `/vulnetix list` | Open the project history screen |
| `/vulnetix status` | Print CLI capabilities as plain text |
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

## Activity drawer

Every `/vulnetix` subcommand — and every CLI probe behind `configure` and
`status` — registers in the right-side activity drawer (`f9`). The drawer shows
what argv ran, live stdout/stderr, and exit state. `x` on a running or queued
row kills the whole process group; a killed subcommand stops the run so the
remaining subcommands never execute. `t` starts `signet:triage-vulns` on the
selected activity's project, keyed per project basename so two projects do not
collide on the instance name. `enter` round-trips the finished output to the
model exactly like a `!shell` result: it classifies first (unless guardrails
are off), seals as a shell attachment, and queues until the transcript is idle
when a turn is in flight.

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
