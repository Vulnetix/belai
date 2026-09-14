# Signet

Signet is a role-managed, injection-safe LLM coding harness. It pairs a Codex-style terminal UI with a safety-first architecture: every harness delimiter carries a cryptographic nonce and integrity hash, untrusted content is classified before it reaches the model, and multiple interaction modes (agent, plan, goal) let you choose the right level of autonomy for the task.

See [docs/nonce-endpoint-spec.md](docs/nonce-endpoint-spec.md) for the provider nonce GET spec.

## Installation

### macOS & Linux (Homebrew)

```bash
brew install vulnetix/tap/signet
```

### Windows (Scoop)

```powershell
scoop bucket add vulnetix https://github.com/Vulnetix/scoop-bucket
scoop install signet
```

### Shell installer

```bash
curl -sSL https://raw.githubusercontent.com/vulnetix/signet/main/install.sh | sh
```

### GitHub Releases

Pre-built binaries for Linux, macOS, and Windows are available on the [Releases](https://github.com/vulnetix/signet/releases) page.
