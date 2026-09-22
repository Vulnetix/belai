# Signet documentation

This directory is the technical reference for Signet. Start with the
[user guide](../README.md) for installation, configuration, and everyday use;
then follow the links below for the security model, operating modes, retry
behaviour, and implementation details.

## Learn Signet

| Topic | Guide | Key sections |
| --- | --- | --- |
| Installation, providers, TUI, CLI, settings, and sessions | [User guide](../README.md) | [Installation](../README.md#installation), [Usage](../README.md#usage), [Configuration](../README.md#configuration), [Settings files](../README.md#settings-files) |
| Security architecture and trust boundaries | [Architecture](architecture.md) | [Delimiter, nonce, and integrity model](architecture.md#delimiter-nonce-and-integrity-model), [Tool-result trust](architecture.md#tool-result-trust), [TUI](architecture.md#tui) |
| Classification, posture gates, permissions, and mode decisions | [Role Manager](role-manager.md) | [Security classification](role-manager.md#security-classification), [Gates and defaults](role-manager.md#gates-and-defaults), [Operating-mode classification](role-manager.md#operating-mode-classification) |
| Language-server diagnostics | [LSP](lsp.md) | [Supported languages](lsp.md#supported-languages), [Security model](lsp.md#security-model), [Settings](lsp.md#settings) |
| Provider retries and recovery | [Resilience](resilience.md) | [Error classification](resilience.md#error-classification-internalresilience), [Turn retry and state invariants](resilience.md#turn-retry-and-state-invariants), [Semantic repair](resilience.md#semantic-repair) |

## Agents

- [Agent Profiles](agent-profiles.md): reusable foreground and background agent
  definitions, schema, lifecycle, autonomy, and the profile builder.
- [Agent Stores](agent-stores.md): read-only search across Signet and other
  agents' session, prompt, and memory stores, including attribution and
  confinement guarantees.

## Integrations and protocols

- [Vulnetix](vulnetix.md): review scanners, artifact handling, project history,
  and the Vulnetix AI Firewall.
- [Nonce endpoint spec](nonce-endpoint-spec.md): the provider/gateway
  `GET /v1/nonces` contract and verification semantics.
- [Image attachments](image-attachments.md): deferred multimodal attachment
  design and candidate terminal-rendering approaches.

## Build, test, and publish

- [Development](development.md): prerequisites, source-running commands, QA
  checklist, tests, versioning, CI, release, and the marketing site.
- The public documentation starts at this index. Repository contributors
  should also read [AGENTS.md](../AGENTS.md) before changing security
  invariants.

## Topic map

- **Safety first:** [trust model](role-manager.md#trust-model) →
  [pipeline](role-manager.md#pipeline) →
  [posture gates](role-manager.md#gates-and-defaults) →
  [tool-result trust](architecture.md#tool-result-trust).
- **Choose autonomy deliberately:** [mode classification](role-manager.md#operating-mode-classification) →
  [plan pass loop](role-manager.md#plan-pass-loop) or
  [goal pass loop](role-manager.md#goal-pass-loop).
- **Run an agent safely:** [profile schema](agent-profiles.md#profile-schema) →
  [background lifecycle](agent-profiles.md#background-agent-lifecycle) →
  [precedence](agent-profiles.md#per-agent-defaults-and-precedence).
- **Recover from provider trouble:** [classification](resilience.md#error-classification-internalresilience) →
  [transport retry](resilience.md#pre-first-byte-boundary) →
  [turn retry](resilience.md#turn-retry-and-state-invariants).
- **Integrate Vulnetix:** [business rules](vulnetix.md#business-rules) →
  [command surface](vulnetix.md#command-surface) →
  [AI Firewall](vulnetix.md#ai-firewall-vulnetix-firewall-and-f10).
