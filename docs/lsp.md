# Language-server diagnostics

Signet can check the file the model just edited and hand any diagnostics back
on the same `Edit`/`Write` result. This happens on the same turn, with no extra
round trip, so the model can fix a syntax error immediately.

- [How it works](#how-it-works)
- [Supported languages](#supported-languages)
- [Security model](#security-model)
- [Settings](#settings)
- [TUI](#tui)
- [Relationship to hooks](#relationship-to-hooks)
- [Fallback behaviour](#fallback-behaviour)
- [Limitations](#limitations)

## How it works

After a `Write` or `Edit` tool runs, the harness reads the edited file and asks
`internal/lsp` for a report. `internal/lsp` either:

1. Uses a language server found on `PATH` (`gopls`, `rust-analyzer`, `clangd`,
   ...).
2. Falls back to a fixed-argv syntax checker (`gofmt -e`, `bash -n`, `ruby -c`,
   ...).

The report is rendered by `internal/lsp/render.go`, sanitized by
`internal/sanitize`, sealed as a `<diagnostics nonce="..." integrity="...">`
block, and appended to the tool result. A clean file produces no block.

Important: the harness **never claims a file is clean**. Silence means the
harness found nothing to report, not that nothing is wrong. A timeout, a still-
warming server, and a clean file all look the same, so none of them is dressed
up as an all-clear.

## Supported languages

| ID | Display | Extensions / names | Primary server | Fallback | Notes |
| -- | ------- | ------------------ | --------------- | -------- | ----- |
| `go` | Go | `.go` | `gopls serve` | `gofmt -e` | |
| `ts` | TypeScript / JavaScript | `.ts .tsx .mts .cts .js .jsx .mjs .cjs` | `typescript-language-server --stdio` | `node --check` (`.js/.mjs/.cjs` only) | covers React, React Native, Node |
| `python` | Python | `.py .pyi` | `pyright-langserver --stdio` → `ruff` → `pylsp` | `python3 -m py_compile` | |
| `rust` | Rust | `.rs` | `rust-analyzer` | `rustfmt --check --edition 2021` | parse errors only |
| `c` | C | `.c .h` | `clangd` | `clang -fsyntax-only` (gated) | shares `clangd` with C++/Obj-C |
| `cpp` | C++ | `.cc .cpp .cxx .hpp .hh .hxx` | `clangd` | `clang -fsyntax-only` (gated) | shares `clangd` with C/Obj-C |
| `objc` | Objective-C | `.m .mm` | `clangd` | `clang -fsyntax-only` (gated) | shares `clangd` with C/C++ |
| `csharp` | C# | `.cs` | `csharp-ls` → `omnisharp` | **none** | |
| `java` | Java | `.java` | `jdtls` | **none** | |
| `dart` | Dart / Flutter | `.dart` | `dart language-server --protocol=lsp` | `dart analyze` | covers Flutter |
| `swift` | Swift | `.swift` | `sourcekit-lsp` | `swiftc -parse` | |
| `zig` | Zig | `.zig .zon` | `zls` | `zig ast-check` | |
| `bash` | Shell | `.sh .bash` | `bash-language-server start` | `bash -n` | |
| `ruby` | Ruby | `.rb .rake .gemspec`, `Rakefile`, `Gemfile` | `ruby-lsp` → `solargraph` | `ruby -c` | |

### Fallback gates

- TypeScript/JSX/TSX have no honest single-file syntax check; `node --check`
  is used only for plain `.js/.mjs/.cjs`.
- C/C++/Objective-C fallbacks require a `compile_commands.json` in the
  confinement root. Without it, `clang -fsyntax-only` produces a wall of missing-
  header noise that looks like real diagnostics, so it is suppressed.
- C# and Java have no useful single-file checker.

## Security model

A diagnostic message is text a third-party binary produced from repository file
contents. It can echo identifiers and string literals. Because the block is
wrapped around server-composed text, it is treated with the same care as an
attachment:

- The harness authors every character outside the message: severity,
  `line:col`, source, and the overflow count.
- Raw LSP JSON never leaves `internal/lsp`. `relatedInformation`, `data`,
  `tags`, and `codeDescription.href` are dropped at decode.
- `source` is restricted to `[A-Za-z0-9._-]` or dropped, and capped at 24
  runes.
- Each message is stripped of Unicode `Cc` and `Cf` runes (controls, bidi, and
  zero-width characters), flattened to one line, and capped at 200 runes.
- The whole report is capped at 10 rows by default.
- The block is sealed with a random nonce and a SHA-256 integrity hash.

The opt-in `lsp.classify_diagnostics` sends only the diagnostics block
through the security classifier, never the surrounding tool confirmation. On a
`models` build with no explicit phase-3 model, this is essentially free.

Language servers read repository configuration by design, and for some servers
that is code execution (`tsconfig.json` plugins, `gopls` running `go list`,
etc.). Therefore:

- Live servers are enabled only in interactive TUI sessions on already-
  trusted roots.
- Headless, non-TTY, and `-trust-dir`-only runs use fallback checks only.
- `workspace/applyEdit` is always answered with `{"applied": false}`.
- `initializationOptions` is always `null`.
- `lsp.servers` is dropped from the project layer unconditionally.

## Settings

```json
{
  "lsp": {
    "enabled": true,
    "fallback": true,
    "classify_diagnostics": false,
    "languages": {
      "go": false
    },
    "timeout_ms": 800,
    "max_diagnostics": 10
  }
}
```

| Key | Default | Project layer |
| --- | ------- | ------------- |
| `enabled` | `true` | may set `false` only |
| `fallback` | `true` | may set `false` only |
| `classify_diagnostics` | `false` | may set `true` only |
| `languages[id]` | auto (on when detected) | `false` honoured; `true` dropped |
| `servers[id]` | none | dropped unconditionally |
| `timeout_ms` | `800` | minimum wins |
| `max_diagnostics` | `10` | minimum wins |

Time values are in milliseconds and validated to `[100, 30000]`; row counts are
validated to `[1, 50]`. Out-of-range values fail the whole settings resolve
closed.

## TUI

`/settings` has a `language servers` submenu. Each row shows:

| Glyph | Meaning |
| ----- | ------- |
| `●` | server detected and enabled |
| `○` | server detected, language turned off |
| `◐` | no server, fallback syntax check exists and enabled |
| `·` | nothing available — install the server or accept no coverage |
| `⋯` | detection in flight |

Keys: `↑↓`/`kj` move, `space` toggle, `x` unset back to auto, `r` re-detect,
`i` install (shows the exact command, `y` to run), `esc` back.

Three new role-manager events appear in the internal-work feed:

- `lsp_detect` — PATH lookup for a language server.
- `lsp_diagnose` — a check ran; verdict is `problems`, `clean`, `warming`,
  `timeout`, or `unavailable`.
- `lsp_server_down` — the live server stopped responding and fell back.

## Relationship to hooks

[Hooks](hooks.md) include `pre_edit`/`post_edit` events, but language-server
diagnostics are **not** wired through them. Hooks are user-authored commands
whose output is arbitrary and is classified as `KindHook`; diagnostics are a harness-shaped, sealed block
from a fixed set of checkers. The two mechanisms stay orthogonal.

## Fallback behaviour

When no server is detected, or when the server is still warming, the harness
runs the fixed-argv fallback if one exists. Fallback output is parsed by the
same renderer and sealed the same way. A timeout counts as a strike and may
reduce the per-key adaptive budget.

Manifest edits (`go.mod`, `package.json`, `tsconfig.json`, `Cargo.toml`,
`pyproject.toml`) notify the server with `workspace/didChangeWatchedFiles` and
return warming, because the server will re-index. Do not wait for diagnostics
after editing a manifest.

## Limitations

- Windows is not supported in this release; the `/settings` row renders
  `unsupported on windows`.
- Monorepos with several confinement roots may evict servers from the warm
  pool cap (6 live connections). Thrashing degrades to "always warming" and then
  to fallback, which is the acceptable floor.
- Newly created files outside the server's workspace folders may not be
  diagnosed by all servers.
