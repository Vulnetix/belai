# `/vulnetix` — Vulnetix review + AI Firewall

`/vulnetix` is the Signet entry point for the Vulnetix CLI and the Vulnetix AI
Firewall. It runs Vulnetix review subcommands, surfaces CLI capability state,
keeps a history of projects found on this machine, and toggles the AI Firewall
for LLM traffic.

## Business rules

- **Never promote arbitrary repository bytes.** SARIF, CycloneDX, and OpenVEX
  artifacts contain raw snippets and paths from the scanned repository. The
  runner composes only metadata into the system transcript (status, exit code,
  artifact, finding count). The bounded report blocks rendered for triage are
  sanitised and then classified by the Role Manager before they are sent as
  file attachments, and are dropped unless the classifier returns
  `ActionProceed`.
- **Scanners are a fixed, allowlisted table.** The nine review scanners —
  `sca`, `containers`, `sast`, `secrets`, `iac`, `malscan`, `sbom`, `aibom`,
  `cbom` — and the post-scan `fix` activity are the only names in
  `AllowedSubcommands`. `VulnetixSettings.Subcommands` is validated at save time
  and at run time against that set. Flags come from the fixed table only; no
  user-supplied flags reach `exec.Command`.
- **Eight of the nine scanners start concurrently.** `sca` and `containers`
  share the `sbom` lane because both write `sbom.cdx.json`, so `containers`
  starts only after `sca` finishes. Every scanner except `sca` passes
  `--disable-memory` so `memory.yaml` has a single writer and SCA keeps its
  finding history and auto-resolve. `sbom` is redirected to
  `inventory.cdx.json` to avoid the other shared file.
- **Review scans have no timeout.** A scan runs until it exits; `x` in the
  runs panel kills that row's process group, and cancelling the parent context
  (esc/quit) cancels every derived subcontext. CLI probes (`version`, `env`,
  `auth`) keep the 15-second default.
- **A killed scanner does not stop the run.** The old loop broke on the first
  non-zero exit; the fan-out lets the remaining scanners finish, and only a
  parent-context cancellation stops all of them.
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
| `/vulnetix` | Run the nine review scanners plus the post-scan `fix` activity, then open the artifacts screen |
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
After a review, the TUI hands the classified report blocks to the live
session as one triage turn:

1. **One subagent per scanner.** Each report is investigated by its own
   read-only explore subagent (`r1`, `r2`, … in the runs panel), on the fast
   tier when one is routed. The report rides to the subagent as a file
   attachment, never in its prompt text. The subagent grounds every finding in
   the repository and reports one line per finding:
   `path:line | rule/id | verdict | remediation`, with `fix:`, `options:`
   (more than one reasonable fix) or `none:` (no fix, and exploring further
   would not settle it).
2. **Reports return to the main thread.** Each subagent report is classified
   and sealed as an `<exploration>` turn, like any explore finding. The main
   model sees all of them, plus the scanner reports as attachments, and
   weighs them against the whole repository.
3. **Clarify on multiple paths.** When a subagent reports `options:`, the
   clarifier asks the user which path to take before remediation starts. The
   answers ride on the user turn as direction.
4. **Switch to the review agent and remediate.** When the review starts its
   triage turn, the session switches to agent mode and engages the built-in
   `signet:vulnetix-review` profile, whatever mode or agent was active. The
   profile's prompt carries the remediation contract. It stays engaged the way
   a picker choice does, so follow-up turns keep the review agent until the
   user clears it (`(none)` in the picker). Dependency findings route to
   `vulnetix fix`: a dry-run plan by default, `--yes` only when
   `vulnetix.autofix` is true. Code findings (SAST, secrets, IaC, container,
   malscan) are patched in the session under the normal permission prompts.
5. **Final report.** The model writes `.vulnetix/signet/code-review-report.md`
   with three sections. *Remediated* lists what was fixed. *Needs direction*
   lists each unresolved choice and its options. *Inconclusive* gives the
   rationale for every finding that has no remediation and that further
   exploration could not decide.

A review that finishes while a turn is running is queued. It is sent on its
own at the next idle, ahead of any queued activity output.

Keys: `↑↓` move, `t` start the built-in `signet:triage-vulns` agent for that
project, `l` history, `esc` back.

## The `Vulnetix` tool

When `vulnetix` is on `PATH` the model gets a first-class `Vulnetix` tool, and
Bash refuses any command that runs the `vulnetix` binary (a mention such as
`grep vulnetix` is fine) with a pointer to the tool. Driving the CLI through
Bash wasted minutes per call: progress bars filled the output, every scan ran
whole-repository reachability, the 120-second Bash default killed scans that
take one to three minutes, `fix` without `--path` failed with "multiple
manifests have autofix candidates", and `fix --yes` edited `go.mod`.

The tool takes the arguments after `vulnetix` and builds the argv itself:

- Only these subcommands run: `scan`, `sca`, `sast`, `secrets`, `iac`,
  `containers`, `malscan`, `sbom`, `aibom`, `cbom`, `license`, `fix`, `vdb`
  lookups (not `cache`, `download`, `poc` or `fetch`), `env`, `version`,
  `auth status`, and `--help` on any of them.
- `fix` always runs as `--dry-run`. `--yes`, `--sca-autofix` and `--jail` are
  refused: the model applies the plan's manifest edits with `Edit`.
- `--no-banner --no-progress --no-analytics` are always added, and
  `--disable-memory` wherever the subcommand accepts it, so `memory.yaml`
  keeps the review's `sca` as its only writer.
- Scans get an explicit `--path` (the working directory unless the model
  names one), which also skips `fix`'s manifest prompt, and
  `--reachability off` unless the model asks for it. The per-subcommand flag
  table follows the CLI's command manifest, so no call fails on an unknown
  flag (`sbom` takes neither `--path` nor `--disable-memory`).
- `--path` and `-o` must stay inside the working tree.
- Scans run one at a time, because they write the same `.vulnetix/`
  artifacts, under a 15-minute limit.
- Output is stripped of ANSI codes, progress bars and spinner redraws, then
  capped at 48 KiB (head and tail), before it is classified as `KindRemote`.
  A footer names the argv that ran, how long it took, and whether exit status
  1 means a gate found something.

Output sent from the runs panel (`⏎` on a row) now carries a directive with
the argv, directory, exit status and duration, and asks the model to diagnose
a failure from the output first instead of rediscovering those facts.

## Dependency hook

Every file a session tool changes (`Write`, `Edit`, `Bash` and the native
catalogue, as seen by the file-diff recorder) is matched against the manifest
table the Vulnetix CLI parses. The table in `internal/depwatch` is a port of
the CLI's `scan.DetectManifest` tables, restricted to the types the CLI can
parse. When `../cli` is checked out next to this repository,
`TestManifestTableMatchesCLI` fails if the two drift apart. Matching is
deterministic: the path and the file's content after the change decide it, and
no file is read.

1. **Coalesce.** Matches are held until the turn ends. A manifest edited
   several times is checked once, against the turn's net change. A change
   undone within the turn, and a deleted manifest, are not checked. A turn
   that fails still flushes the edits it made.
2. **Decide on the fast tier.** The `dep_change` role receives a bounded
   digest of the lines the change removed and added (sanitized, never the
   whole file) and answers `DEPS_CHANGED` or `DEPS_UNCHANGED`. It fails
   toward checking: a malformed reply, a transport error, or a file too large
   or binary to diff is checked anyway.
3. **Check with the CLI.** `vulnetix sca` runs on the manifest's directory
   with `--block-malware --block-eol --block-eol-severity low --exploits poc
   --severity low`, so its exit status says whether anything was found
   (vulnerabilities, public exploits, end-of-life components, malware). The
   CycloneDX output goes to `.vulnetix/signet/deps/`, outside the review's
   artifact list. Memory stays disabled, so `memory.yaml` keeps one writer. On
   a Pro or Enterprise plan (read from `vulnetix auth status` once per
   session) `vulnetix fix --dry-run` adds the Safe Harbour target versions.
   The runs appear in the runs panel.
4. **Triage in the background.** A clean or skipped check is one `deps:`
   line. When the check found something, its reports are sanitized and
   classified, then handed to the background agent for the manifest's
   ecosystem:

   | Profile | Ecosystems |
   | --- | --- |
   | `signet:deps-javascript` | npm, pnpm, Yarn, Deno |
   | `signet:deps-python` | pip, Pipenv, Poetry, uv, Conda |
   | `signet:deps-go` | Go modules |
   | `signet:deps-rust` | Cargo |
   | `signet:deps-ruby` | Bundler |
   | `signet:deps-jvm` | Maven, Gradle, sbt, Mill, Clojure |
   | `signet:deps-dotnet` | NuGet, Paket |
   | `signet:deps-php` | Composer |
   | `signet:deps-apple` | SwiftPM, CocoaPods, Carthage |
   | `signet:deps-containers` | Dockerfile, compose, Kubernetes, Helm, Terraform |
   | `signet:deps-ci` | CI pipelines, GitHub Actions, shell install scripts |
   | `signet:deps-other` | Dart, Elixir, Erlang, Haskell, OCaml, Nix, Conan, vcpkg and the rest |

   Each profile carries its ecosystem's patching reference, drawn from the
   Vulnerability Coordinator package-manager appendix: how to bump a direct
   dependency, how to coerce a transitive (overrides, resolutions,
   constraints, `replace`, `[patch]`, `dependencyManagement` and the like),
   which scopes ship, and the lockfile command. The agents are read-only
   (`Read`, `Grep`, `Glob`). They never install anything or run a package
   manager, so a malicious package's install scripts cannot run on their
   account. Malware is never remediated with a version bump.
5. **Report back.** When the agent finishes, its report is classified and
   queued for the main session like any finished activity. The main model
   decides whether to apply the manifest edits it proposes.

`vulnetix.dep_watch` (default `true`) turns the hook off when set to `false`
in the user's global settings or project prefs. A repo-visible
`.vulnetix/settings.json` may turn it on but never off, so a cloned repository
cannot silence the check on the dependencies it asks you to add.

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
that row's process group; killing one scanner never aborts its siblings, and
only cancelling the whole run (esc/quit) stops all of them. The review scan
rows are registered quiet so their stdout is not round-tripped: the triage
turn carries the structured report attachments instead. `t` starts
`signet:triage-vulns` on the selected activity's project, keyed per project
basename so two projects do not collide on the instance name. `enter`
round-trips the finished output to the model exactly like a `!shell` result:
it classifies first (unless guardrails are off), seals as a shell attachment,
and queues until the transcript is idle when a turn is in flight.

The panel opens on the **activity** tab by default. `f8` opens it on the
**subagents** tab. `tab` cycles through activity → subagents → processes; the
processes tab lists only running supervised processes from the library and
offers `enter`/`v` to view, `x` to stop, and `r` to restart. It is bounded: it
consumes at most one third of the terminal height and refuses to open on
terminals shorter than six rows so the chat input remains usable.

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
