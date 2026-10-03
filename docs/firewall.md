# AI Firewall

An AI Firewall is a gateway between Belai and the model provider. It inspects
each request and can refuse it, redact what it matched, or flag it. Belai
supports several firewalls through **adapters**. Exactly one configured
firewall is **active** at a time, and `F10` turns it on or off for the current
project.

`/firewall` opens the configuration screen (also `f1` then `f`).

| Adapter | Status | What it is |
| --- | --- | --- |
| Vulnetix AI Firewall | stable | Guardrails, redaction and BYOK provider keys for your Vulnetix organisation. Configured by the Vulnetix features (Getting started, `/vulnetix firewall`). |
| Fastly AI Runtime Control | beta | ARC virtual keys and the ARC AI Firewall (log or block mode). |
| Kong AI Gateway | beta | Your Kong route with `ai-proxy` and the AI prompt-guard, sanitizer or guardrails plugins. |
| AI Security Gateway | beta | The self-hosted [aisecuritygateway](https://github.com/aisecuritygateway/aisecuritygateway) (DLP and prompt-injection detection, OpenAI chat completions only). |
| Custom firewall | stable | Any proxy, in one of three modes (below). |
| OpenRouter Guardrails | read-only | OpenRouter's own workspace guardrails. |
| Cloudflare AI Gateway | read-only | The gateway's Llama Guard guardrails and DLP profiles. |

The beta adapters were built from each product's public documentation and
have not been tested against the real product. Fastly does not publish ARC's
endpoint shape, so its adapter is fully configurable and reads refusals
generically.

## Modes

Every adapter except Vulnetix (and the read-only entries) takes a mode:

- **transparent**: only the base URL changes. Belai sends the provider's own
  key in the provider's own header, exactly as it would directly.
- **authorization**: the firewall's key is sent as `Authorization: Bearer`.
  The provider key is never sent, because the firewall holds it (BYOK).
- **header**: the firewall's key is sent in a header you name, for example
  Kong key-auth's `apikey`. The provider key is never sent.

The URL may carry `{provider}`, which is replaced with the provider name, for
example `https://kong.example/ai/{provider}`. As with any base URL,
OpenAI-style providers expect it to end in `/v1` and Anthropic's does not.

The Vulnetix adapter builds its own URL,
`<gateway>/<slug>/<org>/v1` (Anthropic: `<gateway>/anthropic/<org>`). It sends
the Vulnetix API key from the Vulnetix CLI login in the provider's own auth
header. Only the providers the gateway relays are routed; the rest go direct.

## Where things are stored

Instances live in the **global** settings file under `firewall`:

```json
"firewall": {
  "enabled": true,
  "active": "corp-kong",
  "instances": {
    "corp-kong": {"adapter": "kong", "url": "https://kong.example/ai/{provider}", "mode": "header", "header": "apikey", "providers": ["openai", "anthropic"]}
  }
}
```

- The `vulnetix` instance always exists. Its URL comes from its own entry,
  then the legacy `vulnetix.gateway_url`, then
  `https://guardrails.vulnetix.com`.
- An empty `active` means `vulnetix`.
- `providers` limits a firewall to those providers; the rest go direct.
- The legacy `vulnetix.firewall_enabled` key is still read as
  `firewall.enabled`. Belai writes only the new key.

A provider set (the custom `providers` and the `firewall` block) can be kept in the
website's library and installed on another host (see
[library-items.md](library-items.md#providers)). The document never holds a key; the
website's `provider_keys_install` request puts a provider's own key on a host through
the credentials resolver, never into settings, and `provider_keys_remove` clears it
again (see [library-items.md](library-items.md#provider-keys)).

A firewall's own key is never written to settings. The screen stores it with
the credentials resolver under `firewall:<name>`: in the keychain when one is
available, otherwise in the user credentials file. It is read from
`BELAI_FIREWALL_<NAME>_API_KEY`, then the user credentials file, then the
keychain. The repo-visible project credentials file and netrc are never
consulted for a firewall key.

## Rules that keep it safe

- **A repository cannot opt you in.** The project layer
  (`.vulnetix/settings.json`) may set `firewall.enabled: false` and nothing
  else. Its `instances` and `active` are dropped with a note, the same way
  `mcp` is.
- **A firewall key goes only to its own URL.** It is sent only in the
  configured header, to the instance's URL. A routed request never follows a
  redirect: model calls, model lists and nonce fetches all refuse one.
- **URLs are https**, or plain http to a loopback host only.
- **Sign-in providers are never routed.** Copilot, Kiro and the Cloudflare
  AI Gateway provider are never routed through a firewall.
- **Decision backends are never routed.** The local decision model
  (`decision-local`), the hosted `typesafe` provider and self-hosted Jev
  profiles (kind `jev`) answer
  decisions, not chat, so a firewall never carries them and a firewall key
  never rides on a decision request (see docs/role-manager.md, "Decision
  backends"). This includes OpenRouter's Decisions model
  (`typesafe/jev*`) as the classifier: with a firewall routing `openrouter`
  for chat, the classifier still uses your own OpenRouter key, direct to
  OpenRouter. With no such key the selection fails with a message naming
  `OPENROUTER_API_KEY`; the firewall's key is never used for it.
- **Read-only entries change nothing.** OpenRouter and Cloudflare only have
  their refusals read back.
- **`-firewall` / `BELAI_FIREWALL=1`** turn the active firewall on for one
  run, headless included. `BELAI_BASE_URL` still overrides the URL, for tests.

## Cards

When a firewall reports an event, the thread shows a card: red for a block or
refusal, amber for a redaction, flag or strip. A clean pass only counts
towards the footer chip (`firewall: <name> · N events`). Retries of one
refused request fold into one card. Cards are render-only: like every report
card they never reach a model. Every value on a card is third-party text
reduced to cleaned, capped facts (delimiter markup, ANSI, control and bidi
runes are removed). In a headless run each event is one line on stderr, and
stdout stays the reply.

What each firewall reports:

| Firewall | Read from |
| --- | --- |
| Vulnetix | The `X-Vulnetix-Firewall-Decision`, `-Rules`, `-Redactions`, `-Stripped`, `-Request-Id` and `-Nonce` response headers (see [Response headers](https://docs.cli.vulnetix.com/docs/ai-firewall/responses/)). Also the 403 refusal body in the OpenAI or Anthropic envelope (`code`, `blocked_by`, `violations[].policy_name`), and gateway refusals such as `provider_key_missing` or `model_denied`, each with a hint. |
| AI Security Gateway | A 400 `pii_policy_violation` (entity types as rules), 402/403/500 credential and DLP-engine refusals, `x-request-id`, and on non-streamed calls the `aisg_metadata` of a success body (redact or detect). |
| Kong | The opaque 400 `bad request` of `ai-prompt-guard` (reported as a probable prompt-guard block), 403 from the semantic guard and content-safety plugins, and `X-Kong-Request-Id`. |
| Fastly, custom | Any of the shapes above, plus OpenAI- or Anthropic-shaped refusals whose type, code or message names a policy. |
| OpenRouter | A 403 with guardrail `metadata.patterns`, or moderation `metadata.reasons`. `flagged_input` is deliberately not shown. |
| Cloudflare AI Gateway | Error codes 2016/2017 (guardrail blocked the prompt or response), 2029/2030 (DLP), the `cf-aig-dlp` header (FLAG or BLOCK), and `cf-aig-log-id`. |

A matched value is never shown. The Vulnetix gateway never reports one, and
Belai never reads one.

## Nonces

A session's delimiter nonces are fetched from the active firewall first, then
the provider, then minted locally (see
[nonce-endpoint-spec.md](nonce-endpoint-spec.md)). When every nonce comes from
the Vulnetix gateway, requests carry `X-Vulnetix-Nonce-Mode: enforce`, and the
gateway strips sealed blocks it cannot verify. The `/firewall` screen and
`/vulnetix configure` show where the session's nonces come from.

## Commands

| Input | Effect |
| --- | --- |
| `/firewall` | Open the screen: `enter` configures, `space` makes an instance active and turns the firewall on, `x` deletes, `?` shows an adapter's instructions |
| `/firewall on` / `off` | Turn the active firewall on or off for this project (like `F10`) |
| `/firewall use NAME` | Make NAME active and turn it on |
| `/firewall status` | One line: the active firewall, its route for the current provider, the nonce source, and event counts |
| `/vulnetix firewall` | Make the Vulnetix AI Firewall active, or toggle it when it already is |
