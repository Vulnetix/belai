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
- **Web results always go through the classifier.** `WebFetch` and
  `WebSearch` return content written off this machine, so they classify
  unconditionally. Do not add an exemption for them.
- **`Bash` classifies unless a builtin already covers the command.** `cat x`
  returns what `Cat` would have returned, so it is treated the same way;
  anything else — a pipeline, a build, a binary the catalogue does not cover
  — is an arbitrary command and classifies. `tools.BuiltinEquivalent` fails
  closed on shell metacharacters, on `git`/`find`/`env` invocations the
  natives would reject, and on unrecognised binaries. The cloud catalogue
  (`gh`, `aws`, …) is deliberately not exempt.
- **Harness-shaped calls are sanitized only.** `Read`, `Grep`, `Glob`,
  `Write`, `Edit`, and the local native catalogue are built by the harness
  from a fixed argument shape, so they skip the round trip.
- **Plan mode has no Bash.** Plan mode advertises and enforces the same
  narrowed surface (`Registry.Plan` and `modes.ToolAllowed`): no mutating
  tools, and no `Bash` at all, read-only or otherwise.
- **Delimiters are sealed.** Every harness delimiter carries a random nonce
  plus a SHA-256 integrity hash of its enclosed content. On egress, any block
  lacking a nonce, carrying an unknown nonce, or failing its integrity hash is
  stripped before HTTP transport. That includes the `<tools>` briefing, so a
  model cannot widen its own advertised tool surface by writing one.
- **Classifier turns are tool-less.** The classifier payload carries no tools,
  no skills, and no agent block.
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

## Conventions

- Fail closed by default; relaxation is an explicit user opt-in.
- Every new package gets a package doc comment and unit tests.
- Keep package boundaries narrow; reuse `internal/config` for paths and state.
