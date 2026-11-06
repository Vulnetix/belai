# Lint

**Status:** the detection and the sweep ship; the per-edit loop is new.

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

## Per-edit loop

With `lint_loop.enabled` on, the harness lints each file the model has just
written, edited or patched, and returns the findings on the same tool result, so
the model fixes them in the turn that caused them. Language-server diagnostics
(see [language servers](lsp.md)) stay the fast path beside it; the loop adds the
repository's own rule file, which a language server often does not read.

It is off by default, because it runs a program after every edit. Turn it on
with `lint_loop.enabled` in your own settings (the `/settings` row "lint after
edits"), `BELAI_LINT_LOOP=1`, `-lint-loop`, or `/lint-loop on` for one session.
A project file can only turn it off and lower a number.

What runs, and when:

1. The loop starts only after a successful `Write`, `Edit` or `apply_patch`,
   for at most four files of one call, in a trusted directory, and not in plan
   mode. A nested call from code mode does not start it.
2. It needs a detected linter (see [Detection](#detection)) whose tool has a
   one-file form and owns the file's extension. The forms are fixed:

   | Linter | Argv | Files |
   | ------ | ---- | ----- |
   | `lint-ruff` | `ruff check --output-format=concise -- <file>` | `.py`, `.pyi` |
   | `lint-rubocop` | `rubocop --format emacs -- <file>` | `.rb`, `.rake`, `.gemspec` |
   | `lint-golangci` | `golangci-lint run ./<directory>` | `.go` |

   `golangci-lint` analyses packages, so it runs on the file's directory and
   only the rows for the edited file are kept. The other detected linters
   (`cargo fmt`, `cargo clippy`, the `lint` script of ESLint) are
   package-wide and never run on one file. Formatters are out of scope.
3. The operand is a path the harness checked: relative to the repository root
   after symlinks are resolved on both sides, no `..` or empty part, no leading
   `-`, and only letters, digits and `. _ - / @ +`. A file outside the root, or
   with any other character in its name, is not linted.
4. The run uses the same runner as the test suites (see [testing](testing.md)): no
   shell, the scrubbed environment, a process group, the call's OS sandbox policy
   and an output cap. A `Bash` deny rule that matches the argv skips the run. An
   ask rule skips it too, because an edit never stops to prompt: the model gets
   one harness line, once a session, saying the linter was skipped for that
   reason. The bound is `lint_loop.timeout_ms`.
5. The exit code decides what the output means. Only the tool's findings code
   (1 for all three) is read as findings. A timeout, a missing tool, a
   configuration error, a crash or a clean run gives an empty block. The loop never
   tells the model a file is clean.

What the model sees:

- The rows in the linter's output of the form `path:line:col: message` for the
  edited file become diagnostics rows. Everything else a linter prints is
  dropped. A message is flattened to one line, cleaned of control and bidi
  runes and capped at 200 characters; the source is the tool's name from the
  table, never text from the message. At most `lint_loop.max_rows` rows show.
- A linter's messages quote the file, so they are arbitrary text. The rows are
  classified as `tools.KindLint` through the security classifier, always, not
  only when `lsp.classify_diagnostics` is on (guardrails off skips the
  classifier, never the sanitising). Admitted rows are sealed in the same
  `<diagnostics>` block language servers use, with a nonce and a SHA-256, headed
  `lint (ruff)`, and appended to the edit result after it is promoted.
- If the classifier withholds the rows or fails, the model gets one harness
  line with the tool name and a count, and no linter text.
- If the same findings come back for a file `lint_loop.max_attempts` passes in a
  row, the next pass carries a harness note that they are still open and the model
  should move on; later passes are quiet until the findings change or clear. This
  counter is the loop's own and is separate from the language-server triage.

The loop writes `.vulnetix/belai/lint_loop.json`: for each tool, run, finding and
skip counts, the last exit code and the last duration. No path, rule name or
message is kept. The file is written whole through a `.part` name and a symlinked
`.vulnetix` is refused. Each run also leaves a `lint_loop` trace row with the
status, a reason word and the counts.

Reason words are a closed set: `no_linter`, `unsupported_file`, `outside_root`,
`denied`, `needs_approval`, `timeout`, `tool_missing`, `start_failed` and
`tool_error`.

### Settings

| Key | Default | Range | Meaning |
| --- | ------- | ----- | ------- |
| `lint_loop.enabled` | off | | Run the loop. Only an explicit true enables it. |
| `lint_loop.timeout_ms` | 10000 | 1000 to 60000 | Bound on one linter run. |
| `lint_loop.max_attempts` | 3 | 1 to 6 | Passes in a row that may show the same findings before the note. |
| `lint_loop.max_rows` | 10 | 1 to 20 | Rows in one block. |

Your own layers, `BELAI_LINT_LOOP` and the `-lint-loop` flag set any key in
range. The per-project preference file (`belai rc --web-project-settings` key
`lint_loop.enabled`) is yours too and can turn the loop on for one project. A
project settings file can set `lint_loop.enabled` to false and lower a number,
never turn the loop on or raise a number; anything else is ignored with a note.
A value out of range fails the load of a user file and is dropped from a project
file. No profile key and no settings key takes a command: the argv is the table's.

`/lint-loop` shows the state and the numbers, and `/lint-loop on` or
`/lint-loop off` switches it for the session. The choice is held in memory and
is never written to a settings file.

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
