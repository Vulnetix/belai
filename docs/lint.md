# Lint

Belai detects the linters a repository has set up, tells the model how to run
them, and can sweep the repository for drift against the rule files that are
checked in. Nothing here decides what a rule should be: the repository's own
rule file is the authority, and Belai never edits it.

## Detection

`internal/lintdetect` names linters from a fixed marker-file table. It reads
file names and the names of `package.json` scripts only, never a rule file, a
script body or prose, so the result is harness-computed fact and may sit in the
repository map.

| Marker | Linter | Command |
| ------ | ------ | ------- |
| `.golangci.yml` or `.golangci.yaml` | `lint-golangci` | `golangci-lint run` |
| `ruff.toml` or `.ruff.toml` | `lint-ruff` | `ruff check` |
| `rustfmt.toml` or `.rustfmt.toml` | `lint-rustfmt` | `cargo fmt --check` |
| `clippy.toml` or `.clippy.toml` | `lint-clippy` | `cargo clippy` |
| `.rubocop.yml` | `lint-rubocop` | `rubocop` |
| `eslint.config.*` or `.eslintrc*`, and a `lint` script in `package.json` | `lint-eslint` | `npm run lint`, `yarn run lint` or `pnpm run lint` |

Rules and edge cases:

- A manifest is not a rule file. A Go module, a `pyproject.toml` or a
  `Cargo.toml` with no rule file from the table has no detected linter.
- ESLint needs both halves. A config with no `lint` script is not run, because
  the script is the repository's way of giving ESLint its paths and plugins.
  A `lint` script with no ESLint config is not named `lint-eslint`, though the
  repository map still lists the script.
- The package manager comes from the lock file: `pnpm-lock.yaml`, then
  `yarn.lock`, else npm.
- The repository map's `lint:` line lists the detected commands, plus `go vet
  ./...` for a Go module and any `just lint` or `just vet` recipe. It lists
  `npm run lint` only when `package.json` declares a `lint` script.
- The argv is fixed by the table. A model never supplies one, and a profile has
  no key that does.

## The lint sweep

A worker whose profile sets `kanban.lint.sweep` runs the linters once for each
new HEAD, the way `kanban.quality.sweep` runs the test suites. The built-in
`belai:lint-fixer` sets it.


1. The harness reads the detected linters. With none, nothing runs and nothing
   is recorded.
2. It runs each linter's fixed argv through the same runner as the test suites
   (see [testing](testing.md)): no shell, your permission rules, the OS sandbox,
   the scrubbed environment and the `tests.timeout_seconds` bound.
3. It writes `.vulnetix/belai/lint/<commit>.json` (the last twenty are kept):
   each linter's name, tool, argv, rule file, status, exit code, duration, a
   finding count and whether it is drift. A record for HEAD means the linters do
   not run again on that commit. A run in which no linter started (denied, not
   installed) writes nothing and is tried again at the next launch.
4. It files one card for each linter that is drifting, labelled `lint` and
   `lint-drift`, in the backlog, with the title `[lint] <tool> reports N
   findings`. A worker that claims `lint-drift` takes it.

The count is the only part of the linter's output that is kept. It is read from
the tool's own summary or location lines (golangci-lint's `path:line:col:`
lines, ruff's `Found N errors.`, rubocop's `N offenses detected`, `cargo fmt`'s
`Diff in` headers, clippy's `warning:` and `error:` headers, ESLint's `N
problems`). No path, rule name or message reaches a card, a record or a model,
so a hostile file cannot write an instruction onto the board.

A linter is drifting when it counted findings, or when it exited with the code
it uses for findings (1 for golangci-lint, ruff, rubocop, `cargo fmt` and
ESLint) and its output could not be counted, in which case the title omits the
number. Clippy exits 0 on warnings, so only its count marks it. Any other
failing exit code, such as a configuration error, a timeout, a denial or a
tool that is not installed, is not drift and files nothing.

Cards are filed once for each finding. The finding is the linter plus its
count, so relaunching on the same commit, or on a later commit with the same
count, never doubles the board, and a card a person deleted does not come back.
A different count is a different finding. A card is closed, with
`no longer reported`, when a later run of its linter is clean. A card is left
alone when its linter did not run, timed out or is no longer detected.

## Lint gates

A gate on a card may name a detected linter as its suite: `lint-golangci`,
`lint-ruff`, and so on. The gate is met when the linter's fixed argv exits 0 on
the card's branch. A gate is still a reference and never a command. A lint
gate cannot be narrowed with `dir` or `test`. See
[acceptance gates](fleet.md#acceptance-gates).

## Fixing drift

`belai:lint-fixer` claims `lint-drift` cards, detects the language from the
repository map, finds the checked-in rule files and runs the linter or
formatter in its autofix mode over the files it reports. It never edits the
rule file, adds an ignore or disables a rule, and keeps the diff small. Its
branch goes to `belai:reviewer`. See [the maintenance crew](fleet.md#the-maintenance-crew).
