# `/vulnetix review`: the Vulnetix code review

`/vulnetix review` scans the repository in the working directory with nine
Vulnetix CLI scanners, has a read-only agent check each scanner's findings
against the code, and then hands everything to the session, which fixes what
has one clear fix, asks you about what has several, and writes a report.

`/vulnetix` and `/vulnetix run` do the same thing. The command takes no
arguments; what a review runs is set in [settings](#settings).

This page is the user guide. The implementation rules (lanes, artifact
classification, card rendering, severity parsing) are in
[vulnetix.md](vulnetix.md#business-rules).

## Prerequisites

- **The Vulnetix CLI on `PATH`.** Belai looks for `vulnetix` when the review
  starts. Without it the review stops with `vulnetix failed: vulnetix not found
  on PATH`. The [Getting started](vulnetix.md#getting-started) view offers to
  install it (`brew install vulnetix/tap/vulnetix`, or Scoop on Windows), and
  `/vulnetix setup` reopens that view.
- **A model provider.** The scanner agents and the triage turn run on the
  session's model, so the session needs a working provider like any other turn.
- **A Vulnetix credential (optional).** With no credential the CLI runs on its
  built-in [Community](https://www.vulnetix.com/features/community) key. The review still works, but the key's daily quota is
  shared with everyone else using it and scans leave no snapshot on the
  Vulnetix website. Log in from Getting started, or run `vulnetix auth login`,
  for a key of your own. `/vulnetix status` shows what the CLI is logged in as.

## What it runs

The review starts these scanners in the working directory. Each one writes its
results under `.vulnetix/`:

| Scanner | CLI command | Writes |
| --- | --- | --- |
| `sca` | `vulnetix sca` | `sbom.cdx.json` |
| `containers` | `vulnetix containers --disable-memory -o .vulnetix/containers.cdx.json` | `containers.sarif`, `containers.cdx.json` |
| `sast` | `vulnetix sast --disable-memory` | `sast.sarif` |
| `secrets` | `vulnetix secrets --disable-memory --ignore-git` | `secrets.sarif` |
| `iac` | `vulnetix iac --disable-memory` | `iac.sarif` |
| `malscan` | `vulnetix malscan --disable-memory` | `malscan.sarif` |
| `sbom` | `vulnetix sbom --output-file .vulnetix/inventory.cdx.json` | `inventory.cdx.json` |
| `aibom` | `vulnetix aibom --disable-memory` | `ai-bom.cdx.json` |
| `cbom` | `vulnetix cbom --disable-memory` | `cbom.cdx.json` |

After the scanners it runs `vulnetix fix --dry-run --path <workdir>`, or
`vulnetix fix --yes --path <workdir>` when `vulnetix.autofix` is on.

Eight scanners start at once. `containers` waits for `sca`, because both write
`sbom.cdx.json`. Only `sca` writes `.vulnetix/memory.yaml`, so it keeps its
finding history. These flags are fixed: nothing you type and nothing in the
repository reaches the command line.

Two scope limits are worth knowing:

- `secrets` scans the working tree only, not git history. To search history,
  run `vulnetix secrets` yourself.
- `malscan` inspects installed dependencies in install directories directly
  under the repository root (`node_modules`, `.venv`, `vendor` and the like).
  If nothing is installed there, its card says `nothing to scan`.

## How a review proceeds

1. **Start.** The thread shows `▸ vulnetix review started · 9 scanners + fix
   · f9 for output`. The footer counts finished activities and names the ones
   still running. Each scan is a row in the runs panel (`f9`), where `x` kills
   one scanner without stopping the others.
2. **A card per scanner.** As each scanner finishes, a card titled `vulnetix
   <scanner>` appears with its headline counts, its artifact, and a link to the
   scan's snapshot on the Vulnetix website when the CLI is logged in to your
   own organisation. Exit status 1 means the scanner found something; it is not
   a failure.
3. **A read-only agent per scanner.** A scanner with findings immediately starts
   the background agent `belai:vulnetix-scanner` for that scanner. It can use
   `Read`, `Grep` and `Glob` only. For each finding it reads the flagged code
   and its callers, decides whether the finding is real, and replies with one
   line per finding:

   ```
   path:line | rule/id | verdict | remediation
   ```

   The verdict is `real`, `false-positive` or `unclear`. The remediation is
   `fix: <the single change>`, `options: <A> | <B>` when more than one
   reasonable fix exists, or `none: <why>` when neither a fix nor further
   exploration can settle it. Its report appears as a `vulnetix <scanner>
   review` card.
4. **Triage.** When every scan and every scanner agent is done, the thread shows
   `■ vulnetix review done · N issues · <elapsed> · triage starting`, and the
   session switches to agent mode with the built-in `belai:vulnetix-review`
   profile. That turn sees every scanner agent's report and the scanner reports
   as attachments, and weighs them together, since findings from different
   scanners often share a cause.
5. **Clarify.** Where a scanner agent reported `options:`, Belai asks you which
   path to take before any change is made.
6. **Remediate.** For a real finding with one clear fix, the session makes the
   fix. Code findings (SAST, secrets, IaC, containers, malscan) are patched by
   editing files, under the normal permission prompts. Dependency findings go
   through `vulnetix fix`: the session reads the dry-run plan and applies the
   manifest edits itself. It then runs the project's checks. A finding with
   several fixes that your answers do not cover is left for you to decide, and
   a finding nobody could settle is not guessed at.
7. **Report.** The session writes `.vulnetix/belai/code-review-report.md`. See
   [The report](#the-report).

The review profile stays engaged after the review, so follow-up turns keep it
until you clear it (`(none)` in the agent picker).

While a review runs you can type into the composer to steer it: the text is
added to the triage turn as your direction. Scans and scanner agents already
running are not redirected. `esc` cancels the whole review: the scans are
killed, the scanner agents stop, and no triage turn runs.

## How Vulnetix data is used

The SAST, secrets, IaC, container, malscan, AI-BOM and crypto-BOM scanners run
locally with the CLI's built-in rules. Vulnetix vulnerability database (VDB)
data enters the review in three places:

- **`sca`** matches the repository's packages against the VDB. The advisories,
  severities and ratings it finds are written into `sbom.cdx.json`, and the
  card's severity counts come from them.
- **`vulnetix fix`** builds its upgrade plan from the VDB's fixed versions. Even
  as a dry run it queries the VDB; it only skips editing manifests.
- **During triage** the session can make further VDB lookups through the
  [`Vulnetix` tool](vulnetix.md#the-vulnetix-tool) (`vulnetix vdb ...`), and
  through the Vulnetix MCP server's tools if you have added it with
  `/vulnetix mcp`.

These lookups count against your Vulnetix key's daily query limit, the same as
any other CLI call. See the CLI's
[rate limiting](https://docs.cli.vulnetix.com/docs/cli-reference/vdb/#rate-limiting)
reference for the limits and headers.

## What it costs

- **The scanners and the Vulnetix MCP server are free.** They work on the
  [Community plan](https://www.vulnetix.com/features/community).
- **Your model provider bills the model work.** The scanner agents, any explore
  subagents the triage turn starts, and the triage turn itself use your
  configured provider and are billed by it at its rates. With the
  [AI Firewall](vulnetix.md#ai-firewall-vulnetix-firewall-and-f10) on, the
  traffic goes through the gateway but still uses your own provider key.
- **Pro adds Safe Harbour target versions.** On Pro, the fix plan names
  vulnerability-free target versions to upgrade to. On Community, the plan
  says that Pro unlocks them, and dependency fixes are chosen from the fixed
  versions the scan reports.

## The report

The session writes `.vulnetix/belai/code-review-report.md` at the end of the
triage turn. The review profile requires three sections, each naming every
finding by location and rule id:

- **Remediated**: what was fixed.
- **Needs direction**: each finding with more than one reasonable fix that is
  still undecided, with its options.
- **Inconclusive**: each finding with no remediation that further exploration
  could not decide, with the reason.

The model writes the file, so the wording varies between runs; the three
sections do not. A shortened example:

```markdown
## Remediated

- deploy/rds.tf:12 | VNX-TF-003 | publicly_accessible set to false; the instance is reached through the bastion already defined in deploy/network.tf
- go.mod | CVE-2023-44487 | golang.org/x/net raised to the fixed version from the fix plan

## Needs direction

- deploy/main.tf:41 | VNX-TF-001 | S3 bucket with a public ACL. Options: make it private and serve through CloudFront | keep it public for the static site and add a bucket policy limited to GetObject

## Inconclusive

- internal/auth/login.go:30 | VNX-GO-039 | no rate limiting in the Go code; whether the API gateway in front of the service limits /login is not in this repository
```

Belai also writes `code-review-summary.md` and `code-review-manifest.json` to
`.vulnetix/belai/`. Belai's own files are left out of later reviews and out of
`@file` admission.

## Settings

The review reads the `vulnetix` block of `settings.json`
(`~/.vulnetix/belai/settings.json`, or the project's
`.vulnetix/settings.json`):

| Key | Default | Effect |
| --- | --- | --- |
| `vulnetix.subcommands` | every scanner | The scanners to run, from `sca`, `containers`, `sast`, `secrets`, `iac`, `malscan`, `sbom`, `aibom`, `cbom` and `fix`. They always run in the table order above, and `fix` always runs. Any other name is refused when settings load. |
| `vulnetix.timeout` | none | A per-scan time limit, a Go duration from more than zero up to `24h`, such as `10m`. Without it a scan runs until it exits. |
| `vulnetix.autofix` | `false` | `true` runs `vulnetix fix --yes` after the scans instead of `--dry-run`. |

`subcommands` and `timeout` are read from your own settings only, so a
repository cannot narrow the scanners that look at it. A project file can turn
`autofix` off but never on. The full table, including the dependency hook and
the project sweep, is in [vulnetix.md](vulnetix.md#review-settings).

## Examples

Run a full review:

```
/vulnetix review
```

Run only the dependency and code scanners, with a 15-minute limit per scan, in
`~/.vulnetix/belai/settings.json`:

```json
{
  "vulnetix": {
    "subcommands": ["sca", "sast", "secrets"],
    "timeout": "15m"
  }
}
```

Steer the triage while the scans run: type `leave the vendored code under
third_party/ alone` and press enter. The line is passed to the triage turn as
your direction.

The review can also be started by voice: ask for a security review.

## Troubleshooting

| What you see | Cause and fix |
| --- | --- |
| `vulnetix failed: vulnetix not found on PATH` | Install the CLI, or run `/vulnetix setup` to have Belai install it. |
| `a vulnetix review is already running · f9 for output` | One review runs at a time. Wait for it, or press `esc` to cancel it. |
| `settings error: …` at startup | A `vulnetix.subcommands` name or a `vulnetix.timeout` value is invalid. Belai runs on its defaults until the file is fixed. |
| A red scanner card | The scanner timed out, or exited with a status other than 0 or 1. Open the runs panel (`f9`) and press `v` on the row to read its output. |
| `vulnetix <scanner> review timed out after 10m; triage will review it` | The scanner agent took longer than ten minutes and was stopped. The triage turn investigates that scanner's findings itself. |
| `vulnetix <scanner> review produced no report; triage will review it` | The scanner agent returned nothing. The triage turn covers it. |
| `■ vulnetix review done · nothing to triage` | No scanner produced findings that reached triage. There is no triage turn and no report. |
| `malscan` says `nothing to scan` | No dependencies are installed under the repository root. Install them, then review again. |
| No snapshot links on the cards | The CLI is on the shared Community credentials. Run `vulnetix auth login`. |
| `sca` or `fix` fails with `Daily rate limit exceeded` | The key has used its daily VDB query limit. Wait for the reset, or log in with a key of your own if you are on the shared Community key. |
| The fix plan names no Safe Harbour versions | The account is on Community. Safe Harbour target versions need Pro. |
| A card says a report block was withheld | The classifier did not admit that block, so neither the scanner agent nor the triage turn sees it. The artifact is still under `.vulnetix/` for you to read. |
