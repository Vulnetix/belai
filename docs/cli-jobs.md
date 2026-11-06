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
| `gha-audit` | read-only | `vulnetix gha audit --offline --format sarif --output .vulnetix/gha-audit.sarif` | `gha-audit.sarif` |
| `gha-advisories` | mutating | `vulnetix gha publish advisories` | none |
| `gha-code-scanning` | mutating | `vulnetix gha publish code-scanning` | none |
| `gha-dep-graph` | mutating | `vulnetix gha publish dependency-graph` | none |
| `gha-init` | mutating | `vulnetix gha init` | none |
| `gha-setup` | mutating | `vulnetix gha setup --all` | none |
| `iac` | read-only | `vulnetix iac --disable-memory` | `iac.sarif` |
| `ignore-sync` | mutating | `vulnetix ignore sync` | none |
| `image` | read-only | `vulnetix image .vulnetix/image.tar --disable-memory --no-upload --output .vulnetix/image.cdx.json` | `image.cdx.json` |
| `jail` | read-only | `vulnetix jail` | `jail.sarif`, `jail.vex.json` |
| `jail-explain` | read-only | `vulnetix jail explain` | none |
| `license` | read-only | `vulnetix license` | `license.sarif` |
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
| `triage` | read-only | `vulnetix triage --format json --vex-format json --vex-output .vulnetix/triage.json` | `triage.json` |
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
   commits what changed on the item's branch.
   With `kanban.gates.verify: enforce` (the built-in write profile sets it) the
   harness then runs the card's runnable gates on that branch, as it does for any
   other worker (`verifyBranch`), and decides each by exit code. The gates were
   met before the job ran; this decides them again at the new head, so a change
   that breaks a suite is a failed attempt, and a gate that cannot run (a deny or
   ask rule, no binary) blocks the card for a person. Verification runs only when
   the job changed files.
   The card is then released to review with the branch recorded. The harness
   pushes that branch to `origin` itself when `agents.publish` and the
   profile's `workspace.publish` allow it (a model worker's own tool does that
   for the other profiles), because the Software Factory opens the draft pull
   request from the card's branch and finds nothing when the branch is only
   local. A failed push is said in the release note and is not a failed attempt;
   the pull request is the factory's to open, and the harness never opens one for
   a card that goes to review. A profile that routes to `done` is published by the
   harness as any worker is.
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

## What a job reports

| Jobs | Reader | Finding kind and fields |
| --- | --- | --- |
| `sca`, `sast`, `secrets`, `iac`, `malscan`, `scan` | review artefacts that record HEAD | `sca`, `sast`, `secrets`, `iac`, `malscan`: advisory id or rule id, path, severity |
| `containers` | SARIF | `container`: rule id, path, severity |
| `license` | SARIF | `license`: rule id, manifest path, severity |
| `jail` | SARIF | `jail`: rule id, path, severity |
| `gha-audit` | SARIF | `gha`: rule id, workflow path, severity |
| `triage` | triage JSON summary | `triage`: advisory id, severity, VEX status |
| `cbom` | CycloneDX | `crypto`: algorithm name, post-quantum status; quantum-vulnerable is `high`, deprecated is `medium` |
| `aibom` | CycloneDX | `ai`: component name, category |
| `sbom`, `image` | CycloneDX | vulnerability counts only |
| `scout` | fixed detector | `signals` |

Every other job records the exit code and the artifacts present.

## Fixer profiles

The Software Factory turns a job's findings into cards, and a model worker fixes
what the CLI can report but not repair. Each fixer claims one card label, works
on a worktree branch, is held to the card's gates by the harness
(`kanban.gates.verify: enforce`, except `belai:defend-writer`, whose rules no
suite runs) and releases to review labelled `needs-review`. The Software Factory
then opens the draft pull request from the card's branch. None of them merges,
weakens a test or a policy, adds an exception, or reaches the web or GitHub.

| Profile | Claims label | Card is filed from | Reuses |
| --- | --- | --- | --- |
| `belai:patcher` | `vuln` | `sca` and `scan` findings (security crew) | existing |
| `belai:verifier` | `needs-verify` | the patcher's branches | existing |
| `belai:secret-triager` | `sf:secret-triage` | `secrets` findings | existing |
| `belai:workflow-hardener` | `sf:workflow-review` | workflow changes | existing |
| `belai:workflow-fixer` | `sf:fix-workflow` | `gha-audit` findings | existing |
| `belai:pqc-fixer` | `sf:pqc` | `cbom` findings (`crypto` kind) | new |
| `belai:iac-fixer` | `sf:fix-iac` | `iac` findings | new |
| `belai:container-fixer` | `sf:fix-container` | `containers` findings | new |
| `belai:license-fixer` | `license` | `license` findings | new |
| `belai:defend-writer` | `sf:no-fix` | advisories with no fixed release | new |

The Software Factory and the console mirror this list: the five new profile
names and the labels above are the contract, and a test in this repository pins
them (`TestFactoryFixerProfiles`). The label `license` has no `sf:` prefix. Labels are lower-cased, limited to
`a-z0-9:_-` and cut at 32 characters, and every label here is already in that
form.

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
- A job without a reader (the `none` rows above) records the exit code and the
  artifacts present, with no findings.
- `image` needs an image archive (`docker save` or `podman save`) at
  `.vulnetix/image.tar`, put there by an earlier stage. The CLI takes an image
  reference or archive as an argument and the table cannot carry one from a card,
  so an archive at a fixed path is the only input; with none the result is exit 3.
- `triage` runs the CLI's own triage over the open findings in `.vulnetix`
  memory and writes the JSON summary with `--vex-format json`, which is not
  uploaded. The reader keeps the advisory id, the severity word and the VEX status
  of each finding that is not `not_affected` or `fixed`; the package, version,
  justification and action text are not copied.
- `defend` takes an advisory id on the CLI (`vulnetix defend <advisory-id>`) and
  a card's text may not reach an argument, so the table's `defend --quiet` has no
  id to give. The `belai:defend-writer` worker runs `vulnetix defend <id>` itself
  for the advisory its card names, under the permission rules and the sandbox.
