# AGENTS

Guidance for coding agents (and humans) working in this repository.

## Build and test

Tasks live in the `justfile`; run `just` to list them. There is no Makefile —
`make` cannot forward arguments, which forced developers onto stale prebuilt
binaries. Everything below runs from source.

- `just check` must pass. It is `gofmt` + `go vet ./...` + `go test -race ./...`,
  in the same order as `.github/workflows/ci.yml`.
- `just build` must succeed.
- `just prompt "…"` / `just ask PROVIDER MODEL "…"` drive the CLI from source.
- `just e2e` drives the built binary against a mock provider.

See [docs/development.md](docs/development.md) for the full local and QA workflow.

## Security invariants — do not weaken

- **Untrusted content stays untrusted.** Every tool result is sanitized
  (delimiter markup removed) before it can be promoted, and none of them ever
  enters a system/agent/tools block.
- **Arbitrary content goes through the classifier.** `Bash` (an arbitrary
  command), `WebFetch` and `WebSearch` (text written off this machine),
  `Read` (a file's bytes), `GH`/`Glab` results (`KindRemote`, third-party
  repository text), and `RepoRead` (`KindRead`) all classify,
  unconditionally. Do not add an exemption for any of them.
- **Shaped, controlled results are sanitized only.** `Grep`, `Glob`, `Write`,
  `Edit`, and the native catalogue return output whose shape the harness
  knows — `path:line:text`, a list of paths, a confirmation it composed
  itself, a fixed argv's output — so they skip the round trip. A kind absent
  from `tools.classifierKinds` is sanitize-only, so adding a tool whose
  content is arbitrary means adding its kind there.
- **The repo map is harness-computed facts only.** It may contain paths,
  counts, detected commands, git metadata and file sizes, and never repository
  file contents. Repository prose reaching the model stays on the
  `RepoRead`/`Read` path, which classifies. This is what permits the map in the
  system block.
- **Plan mode has no Bash by default.** Plan mode advertises and enforces
  the fail-closed surface (`Registry.PlanWith` and `modes.ToolAllowed`):
  no mutating tools and no `Bash`. A read-only `Bash` returns only when an
  explicit permission allow rule opts into it, and guardrails off restores the
  full surface.
- **Delimiters are sealed.** Every harness delimiter carries a random nonce
  plus a SHA-256 integrity hash of its enclosed content. On egress, any block
  lacking a nonce, carrying an unknown nonce, or failing its integrity hash is
  stripped before HTTP transport. That includes the `<tools>` briefing, so a
  model cannot widen its own advertised tool surface by writing one.
- **Classifier turns are tool-less.** The classifier payload carries no tools,
  no skills, and no agent block.
- **The guardrails switch reaches every surface.** Off means
  `posture.AllIgnore()` everywhere — agent session, inline `!cmd`, `@file`
  admission, background agents, and the CLI. Derive it from
  `App.effectivePosture()` in the TUI or `settings.GuardrailsEnabled()` in
  `cmd/signet`; never read `a.posture` directly and never hand-roll the
  all-ignore loop. A gated path checks the level **before** calling the
  classifier, never after — a verdict that cannot change the outcome is a
  request nobody asked for and sends the content anyway. Sanitising is not
  part of the switch and always runs.
- **Tool-call mismatch defaults to abort.** Stripping or ignoring mismatches
  requires explicit user opt-in.
- **Skills and hooks validate first.** Skills load only after strict
  front-matter schema validation; hooks load only after schema validation with
  no arbitrary code-path injection.

## Layout

- `cmd/signet` — entrypoint.
- `internal/...` — library code, one package per concern.
- `e2e/` — end-to-end tests that drive the built binary.
- `docs/` — architecture, specs, and the development workflow.

## Sentinel values

`internal/rolemanager` defines the strict single-token outputs for the
security classifier, mode classifier, plan evaluator, and goal evaluator.
Each sentinel has a corresponding human-readable label used by the TUI:

- **Security sentinels:** `internal/rolemanager/labels.go` — `SentinelLabels`
- **Plan sentinels:** `internal/rolemanager/labels.go` — `PlanSentinelLabels`
- **Goal sentinels:** `internal/rolemanager/labels.go` — `GoalSentinelLabels`

When adding or editing a sentinel constant, update the matching label map and
its `Label()` method test in `internal/rolemanager/labels_test.go` so the
TUI never falls back to the raw token. The TUI should call `.Label()` rather
than `string()` or `%s` on the sentinel value.

## Conventions

- Fail closed by default; relaxation is an explicit user opt-in.
- Every new package gets a package doc comment and unit tests.
- Keep package boundaries narrow; reuse `internal/config` for paths and state.
- **Align tool names and schemas to existing harnesses.** Models are trained
  on `ExitPlanMode`, `update_plan`, `Read`, `Grep`, `WebFetch`. A trained name
  with a trained argument shape is obeyed more reliably than an equivalent
  invented one, so a new tool takes the established name and schema unless no
  equivalent exists. Document any deliberate divergence in the tool description
  (as `update_plan`-in-plan-mode does).
