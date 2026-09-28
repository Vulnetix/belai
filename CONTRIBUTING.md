# Contributing to Belai

Thanks for contributing. Belai is a safety-first LLM coding harness; the rules
below keep changes reviewable and the security model intact.

## Before you start

Read [AGENTS.md](AGENTS.md). It defines the security invariants a change must
not weaken, plus the coding conventions. The full development workflow lives in
[docs/development.md](docs/development.md); [docs/README.md](docs/README.md)
indexes the rest of the documentation.

Features are documented before they are built — see the Roadmap in
[docs/README.md](docs/README.md). A new feature should land with its own entry
in [docs/](docs/), not as an explanation buried in code or a pull request.

## Making a change

1. Make the smallest change that does the job, in the style of the surrounding
   code. Match `gofmt` and the package-doc/unit-test conventions in AGENTS.md.
2. Run `just check`. It runs `gofmt`, `go vet ./...`, `go test -race ./...`,
   and the cross-compile, in the same order as CI — green locally means green
   in CI. `just build` must also succeed, and `just e2e` drives the built
   binary against a mock provider.
3. Push and open a pull request. CI runs on every push and pull request.

## Commit messages

Follow conventional commits, with a scope where it helps:

```
feat(fleet): run agent workers from the kanban board
fix(tui): keep the footer's second line within the terminal width
docs(site): add a web sessions section
```

## Questions

Open an issue, or look at the existing documentation first — most of how the
project works is already written down in [docs/](docs/) and
[AGENTS.md](AGENTS.md).