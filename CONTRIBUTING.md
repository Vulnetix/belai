# Contributing to Belai

Thanks for contributing. Belai is a safety-first LLM coding harness, and the
invariants in [AGENTS.md](AGENTS.md) are load-bearing: read them before you
change anything, and treat weakening one as a hard blocker.

## Getting started

1. Fork and clone, then install the prerequisites listed in
   [docs/development.md](docs/development.md#prerequisites) (Go and `just`).
2. `just` lists every recipe; `just tui` runs the harness from source.
3. Make your change on a branch and keep it small and focused.

## Before you open a PR

`just check` must pass. It runs `gofmt`, `go vet ./...`,
`go test -race ./...`, and a windows/amd64 + darwin/arm64 cross-compile — the
same steps as CI (`.github/workflows/ci.yml`). Also:

```bash
just build   # the host binary, stamped with the release ldflags
just e2e     # drives the built binary against a mock provider
```

Green locally means green in CI. See [docs/development.md](docs/development.md)
for the full test matrix (`just test`, `just test-race`, `just cover`), the
manual QA checklist, and the lint recipes (`just fmt`, `just vet`, `just tidy`).

## Conventions

- **Docs-first.** Features are documented in `docs/` before they are built;
  the [Roadmap](docs/README.md#roadmap) marks a feature `Roadmap` until it
  ships, then `alpha-YYYYMMDD`. Add the design doc alongside the code and link
  it from [docs/README.md](docs/README.md).
- **Security invariants.** [AGENTS.md](AGENTS.md) lists what a change must not
  weaken. Read it rather than relying on a summary here.
- **Commit messages.** Conventional commits, lowercase scope, imperative mood:

  ```
  feat(tui): …
  fix(fleet): …
  docs(site): …
  ci(release): …
  ```

- **Go hygiene.** New packages get a package doc comment and unit tests. Run
  `just fmt` before committing and keep package boundaries narrow.

## Where things live

- `cmd/belai` — entrypoint and flag parsing.
- `internal/…` — library code, one package per concern.
- `e2e/` — end-to-end tests that drive the built binary.
- `docs/` — architecture, specs, and the development workflow (start at
  [docs/README.md](docs/README.md)).
- `site/` — the marketing site ([docs/site.md](docs/site.md)).

## Questions?

Read [docs/development.md](docs/development.md) and [AGENTS.md](AGENTS.md)
first — they answer most workflow and security questions. Otherwise open an
issue.