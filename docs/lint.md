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

