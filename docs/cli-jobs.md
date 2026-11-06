# CLI jobs

**Status:** new.

A worker can run a Vulnetix CLI job for a card. The card is labelled
`cli-job:<id>`, the id is one of a closed table, and the harness (not a model)
runs the job's fixed argument list, reads the files it leaves and records a
result of ids, counts and enums on the card. The Software Factory uses this to
scan a repository without a clone it owns: it files a card and reads the
result from the card.

Nothing here asks a model whether a job ran, what it found or whether a
change may happen.

## Profile keys

| Key | Meaning |
| --- | --- |
| `kanban.cli.jobs` | The job ids the profile may run. Each must be in the table below. |
| `kanban.cli.write` | Allows mutating jobs in the list. Needs `workspace.isolation: worktree`. |

A profile that sets `kanban.cli` claims only cards that carry a
`cli-job:` label, and cannot combine the block with `kanban.conflicts`,
`survey`, `security` or `candidates`. A project layer defines no profile, so a
repository cannot add either key. The built-in profiles are:

| Profile | Jobs |
| --- | --- |
| `belai:cli-runner` | Every read-only job, including `scout`. Done on success. |
| `belai:cli-runner-write` | Every mutating job, in the item's worktree. Review on success, labelled `needs-review`. |

## The job table

Each row is one entry of `internal/clijobs`. The argument list is fixed words and
fixed paths under `.vulnetix`; the harness adds `--no-banner`, `--no-progress`
and `--no-analytics`. No card text, model argument or setting reaches an
argument. A test pins the ids and the read-only versus mutating split to this
page, and checks every subcommand and flag against the CLI's own command
manifest when the CLI checkout is next to this one.

A read-only job changes nothing outside `.vulnetix`. A mutating job changes a
file in the repository, the forge or a Vulnetix organisation.

| Job | Kind | Fixed argv (after `vulnetix`) | Artifacts read |
| --- | --- | --- | --- |
| `ai-firewall-apply` | mutating | `vulnetix ai-firewall apply --file .vulnetix/ai-firewall.yaml` | none |
| `ai-firewall-status` | read-only | `vulnetix ai-firewall status` | none |
| `aibom` | read-only | `vulnetix aibom --disable-memory --no-upload` | `ai-bom.cdx.json` |
| `analyze` | read-only | `vulnetix analyze --no-upload --output-file .vulnetix/analyze.report.json` | `analyze.report.json` |
| `api-discover` | read-only | `vulnetix api discover --no-upload` | none |
| `api-upload` | mutating | `vulnetix api upload` | none |
| `attest-verify` | read-only | `vulnetix attest verify --envelope .vulnetix/attestation.json` | none |
| `bom-diff` | read-only | `vulnetix bom diff .vulnetix/baseline.cdx.json .vulnetix/inventory.cdx.json` | none |
| `bom-enrich` | mutating | `vulnetix bom enrich .vulnetix/inventory.cdx.json` | none |
| `bom-merge` | mutating | `vulnetix bom merge --out .vulnetix/merged.cdx.json .vulnetix/sbom.cdx.json .vulnetix/inventory.cdx.json` | `merged.cdx.json` |
| `bom-skew` | read-only | `vulnetix bom skew --from .vulnetix` | none |
| `bom-validate` | read-only | `vulnetix bom validate .vulnetix/inventory.cdx.json` | none |
| `cbom` | read-only | `vulnetix cbom --disable-memory --no-upload` | `cbom.cdx.json` |
| `config-apply` | mutating | `vulnetix config apply --file .vulnetix/config.yaml --auto-approve` | none |
| `config-plan` | read-only | `vulnetix config plan --file .vulnetix/config.yaml` | none |
| `containers` | read-only | `vulnetix containers --disable-memory -o .vulnetix/containers.cdx.json` | `containers.sarif`, `containers.cdx.json` |
| `defend` | read-only | `vulnetix defend --quiet` | none |
| `fix` | mutating | `vulnetix fix --yes` | none |
| `gha-audit` | read-only | `vulnetix gha audit --offline` | none |
| `gha-init` | mutating | `vulnetix gha init` | none |
| `gha-advisories` | mutating | `vulnetix gha publish advisories` | none |
| `gha-code-scanning` | mutating | `vulnetix gha publish code-scanning` | none |
| `gha-dep-graph` | mutating | `vulnetix gha publish dependency-graph` | none |
| `gha-setup` | mutating | `vulnetix gha setup --all` | none |
| `iac` | read-only | `vulnetix iac --disable-memory` | `iac.sarif` |
| `ignore-sync` | mutating | `vulnetix ignore sync` | none |
| `image` | read-only | `vulnetix image --disable-memory --no-upload` | none |
| `jail` | read-only | `vulnetix jail` | none |
| `jail-explain` | read-only | `vulnetix jail explain` | none |
| `license` | read-only | `vulnetix license` | none |
| `malscan` | read-only | `vulnetix malscan --disable-memory --no-upload` | `malscan.sarif` |
| `report` | read-only | `vulnetix report` | none |
| `sast` | read-only | `vulnetix sast --disable-memory` | `sast.sarif` |
| `sbom` | read-only | `vulnetix sbom --output-file .vulnetix/inventory.cdx.json` | `inventory.cdx.json` |
| `sca` | read-only | `vulnetix sca` | `memory.yaml`, `sbom.cdx.json` |
| `scan` | read-only | `vulnetix scan` | `memory.yaml`, `sast.sarif`, `secrets.sarif`, `iac.sarif`, `malscan.sarif`, `sbom.cdx.json` |
| `scout` | read-only | `vulnetix agent capabilities`; `vulnetix scan --dry-run --show-detected`; `vulnetix analyze --no-upload --output-file .vulnetix/analyze.report.json` | `capabilities.yaml`, `analyze.report.json` |
| `secrets` | read-only | `vulnetix secrets --disable-memory --ignore-git` | `secrets.sarif` |
| `surface-liveness` | read-only | `vulnetix surface liveness` | none |
| `tea-release` | mutating | `vulnetix tea release --auto-version` | none |
| `triage` | read-only | `vulnetix triage status --format json` | none |
| `vex-apply` | mutating | `vulnetix vex apply --bom .vulnetix/sbom.cdx.json --vex .vulnetix/vex --out .vulnetix/sbom.effective.cdx.json` | `sbom.effective.cdx.json` |

`scout` is a composite: it runs the three steps in order and emits `signals`
from a fixed detector (see [Signals](#signals)).

A job with an input file (`bom-diff`, `bom-merge` and others) needs that file
at its fixed path. When it is missing the job does not run, and the result has
exit code 3.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | The job ran and found nothing that breaches its gate. |
| 1 | A gate breach. |
| 3 | Not evaluable (an input is missing or the CLI could not decide). |

The result carries the first step's code that is not 0. A code is data, not a
failure of the worker: a gate breach still releases the card as a success so
the reader of the result decides. A job that could not start, timed out or was
refused by a permission rule is not a result. A refusal blocks the card with a
harness-composed note; an error or timeout counts as a failed attempt.

## How a card is run

1. The harness claims a backlog card that carries a `cli-job:<id>` label (a
   prefix match, so one profile serves every id).
2. The id must be in the profile's `kanban.cli.jobs`, else the card is blocked
   with a note naming the rule.
3. A mutating job also needs `kanban.cli.write` and a card whose acceptance gates
   are all met (at least one gate). Otherwise the card is blocked. It then
   runs in the item's worktree, the scan output is removed, and the harness
   commits what changed on the item's branch and releases the card to review.
4. The argv runs through `internal/testrun`: no shell, the user's permission rules
   (a deny or ask rule blocks the card), the OS sandbox, the scrubbed environment
   and the `tests.timeout_seconds` bound. A read-only job runs in the trusted
   repository root.
5. The harness reads the job's artifacts through `internal/scanartifacts`, builds
   the result and records it at release.

No model turn runs for any of this. The release note holds the job id, the exit
code and counts only.

## Result shape

The result is a JSON object stored on the card (`CliResult`, synced as the
object `agent.cliResult`):

```json
{
  "job": "sca",
  "exitCode": 0,
  "counts": {"critical": 0, "high": 0, "medium": 0, "low": 0},
  "findings": [
    {"id": "CVE-2024-0001", "kind": "sca", "path": "go.mod", "severity": "high", "ref": "pkg@1.2.3"}
  ],
  "signals": ["crypto"],
  "artifacts": ["cbom.cdx.json"]
}
```

- `findings` holds at most 500 entries. `counts` still counts every finding, so a
  truncated list does not hide volume.
- Every field of a finding matches a fixed pattern: an identifier, a kind word, a
  repository-relative path without `..`, a severity word (`critical`, `high`,
  `medium`, `low`, `info`, `unknown`) and a short reference such as a rule id or
  `package@version`.
- Scanner messages, advisory prose, matched text and secret values never enter
  it.
- The document is at most 192 KiB. The harness alone sets it. A pulled copy
  fills an empty field only after the same validation (`clijobs.Clean`), never
  replaces or clears a local value, and the website cannot set it.

## Signals

`scout` emits `signals` for the deep signals only: `crypto`, `ai-sdk`,
`agent-config`, `installed-deps` and `release-artifacts`. The detector matches
fixed lower-case tokens against three views: the true keys of the `repo` map in
`.vulnetix/capabilities.yaml`, the lines of the `scan --show-detected` listing,
and the metric ids of the analyze report. It emits a signal name when a token
matches and keeps no text. A token it does not know is dropped, and so is any
signal outside the closed set of seventeen that the Software Factory profile
defines.

## Limits

- A mutating job is gated by the card, and the Software Factory also holds it
  behind its own autonomy level. Gates live on the host that decided them and
  are never carried by a pulled card, so a mutating card is run on the host
  that gated it.
- `image`, `license` and the other jobs without a reader record the exit code and
  the artifacts present, with no findings.
