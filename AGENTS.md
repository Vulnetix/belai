# AGENTS

Guidance for coding agents (and humans) working in this repository.

## Build and test

- `go build ./...` must succeed.
- `go test ./...` must pass; CI runs `go test -race ./...`.
- `gofmt -l .` must print nothing.
- `go vet ./...` must pass.

## Security invariants — do not weaken

- **Untrusted content stays untrusted.** Read/WebSearch/WebFetch output is
  sanitized (delimiter markup removed) and then run through a tool-less
  classifier before it can be promoted. It never enters system/agent blocks.
- **Delimiters are sealed.** Every harness delimiter carries a random nonce
  plus a SHA-256 integrity hash of its enclosed content. On egress, any block
  lacking a nonce, carrying an unknown nonce, or failing its integrity hash is
  stripped before HTTP transport.
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
- `docs/` — architecture and specs.

## Conventions

- Fail closed by default; relaxation is an explicit user opt-in.
- Every new package gets a package doc comment and unit tests.
- Keep package boundaries narrow; reuse `internal/config` for paths and state.