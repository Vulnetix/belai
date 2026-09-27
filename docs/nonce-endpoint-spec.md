# Nonce endpoint spec

Belai seals every harness-generated delimiter with a random nonce plus a
SHA-256 integrity hash of the enclosed content. Belai can mint its own nonces
from a CSPRNG pool. An AI Firewall or a provider may instead supply them. When
the Vulnetix AI Firewall supplies them, the gateway also verifies the sealed
blocks it forwards (see *Gateway verification*).

## Endpoint

```
GET {base_url}/v1/nonces[?count=N]
```

`{base_url}` is the firewall or provider base URL without the surface version
prefix. OpenAI-style base URLs already end in `/v1`; the trailing `/v1` is
normalised so the endpoint is always exactly `/v1/nonces` (never
`/v1/v1/nonces`). Anthropic-style base URLs carry no `/v1`, so the endpoint is
appended directly. `count` asks for that many nonces (the Vulnetix gateway
accepts 1–64 and defaults to 16). An endpoint may ignore it.

Examples:

| Base URL                                           | Nonce endpoint                                                  |
| -------------------------------------------------- | --------------------------------------------------------------- |
| `https://api.openai.com/v1`                        | `https://api.openai.com/v1/nonces`                              |
| `https://api.anthropic.com`                        | `https://api.anthropic.com/v1/nonces`                           |
| `https://guardrails.vulnetix.com/openai/<org>/v1`  | `https://guardrails.vulnetix.com/openai/<org>/v1/nonces`        |
| `https://guardrails.vulnetix.com/anthropic/<org>`  | `https://guardrails.vulnetix.com/anthropic/<org>/v1/nonces`     |

## Probe order

A session asks, in order:

1. **The active AI Firewall**, when one routes the session's provider (see
   [firewall.md](firewall.md)), with the firewall's own credential: its key
   header, or the Vulnetix API key as a Bearer token.
2. **The provider itself**, with its key as a Bearer token. This happens only
   when that key would reach it anyway: with no firewall, or through a
   transparent one. A BYOK firewall holds the provider key, so the provider
   is never contacted directly.
3. **Local minting**, when neither answers.

The first endpoint that answers `200` with nonces seeds the pool, and the pool
is then **remote**. A remote pool refills from the same endpoint, 64 at a
time, when it runs dry, and `Rotate` refetches from it. If a refill fails, the
pool turns **local** for the rest of the session and mints locally, so a
sealed block is never left without a nonce.

A nonce fetch never follows a redirect: its credential is for that endpoint
alone.

Subagents skip the probe entirely. They discard the provider-seeded pool for a
fresh local one immediately after construction, so they seed locally and the
round trip is never made.

## Request

The request carries the endpoint's credential (see *Probe order*) and Belai's
`user-agent` (`belai/<version> (+https://github.com/Vulnetix/belai)`).
Provider turns also carry the `X-Belai-Session-Id`, `X-Belai-Client-Version`,
`X-Belai-Client-Build` and W3C `traceparent` headers described in
[architecture.md](architecture.md#outbound-identification-and-trace-headers).
An endpoint may log them for correlation but must not depend on them.

## Response (200 OK)

A JSON object with a list of nonces:

```json
{
  "nonces": [
    "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
  ],
  "count": 2,
  "expires_at": "2026-09-28T10:00:00Z"
}
```

- `nonces` — array of nonce strings (opaque, hex-encoded random values).
- `count` — number of nonces in the list.
- `expires_at` — optional; when the issuer stops accepting them (the
  Vulnetix gateway: 24 hours after issue).

`nonces` is authoritative. `count` is decoded but never enforced, so a
response whose `count` disagrees with the array length is accepted and the
array wins. A `200` carrying an empty `nonces` array is accepted as an answer
but seeds nothing, and the probe moves on to the next endpoint.

Fetched nonces are *appended* to the pool's available list rather than
replacing it, so seeding never discards nonces the pool already holds. Only
`Rotate` discards.

## Unsupported, failing, and the negative cache

An endpoint that does not implement nonces, or has them disabled, returns
`401` (or `400`/`403`/`404`/`405`). Belai treats any of these as
**unsupported**, caches that verdict for **7 days**, and moves on to the next
endpoint.

A **transient** failure is cached for **30 minutes**, during which the
endpoint is not asked at all. Transient failures are a 5xx, a transport error,
the 3-second timeout, or a `200` whose body is not valid JSON. This keeps
repeated sessions from re-probing an endpoint that is down.

Both verdicts are held in process and, keyed by a hash of the base URL, in
`~/.vulnetix/belai/cache/nonce-unsupported.json`. A `200` is never cached. A
change to a firewall's configuration clears the verdicts for the affected
URL.

`SeedFromProvider`, the single-endpoint form, still returns a non-unsupported
failure to its caller instead of minting locally. The session's probe chain
always ends in local minting.

## Verification semantics (Belai)

Nonces fetched from an endpoint are added to the harness's nonce pool. The
delimiter engine accepts a block only when its nonce is currently reserved in
the pool. A nonce that is merely *available* (fetched but never reserved) is
not accepted. Rotating the pool invalidates all previously issued nonces.

## Gateway verification (Vulnetix AI Firewall)

The Vulnetix gateway's nonces are signed, not stored. Each is random bytes
plus a 24-hour expiry, authenticated with an HMAC keyed by the calling
principal's own secret. Any gateway replica can verify one, nothing is
written anywhere, and a nonce issued to one principal never verifies for
another. On each inference request the gateway checks every sealed harness
block (`<kind nonce="…" integrity="…">…</kind>` for the harness's known
kinds). Each block is one of:

- **verified**: the nonce was issued to this organisation and is unexpired,
  and the integrity hash matches the content;
- **unknown**: the nonce was never issued to this organisation, or has
  expired;
- **tampered**: the integrity hash does not match, or a kind that requires it
  (`attachment`, `directive`, `diagnostics`) carries none.

What happens next depends on the client's request header:

- `X-Vulnetix-Nonce-Mode: enforce` asks the gateway to **strip** unknown and
  tampered blocks before forwarding. Belai sends it only while its pool is
  remote and was seeded by this gateway, i.e. every nonce it reserved came from
  there.
- Without the header the gateway **observes**: it counts, and strips nothing.
  A client that mints its own nonces is never broken.
- An organisation guardrail of rule type `delimiter_integrity` with action
  `block` refuses a request carrying a tampered block, with code
  `delimiter_tampered`.

The gateway reports the result on every response:

```
X-Vulnetix-Firewall-Nonce: mode=enforce;verified=12;unknown=0;tampered=1;stripped=1
```

Belai shows a card when an enforcing gateway stripped or found tampered
blocks.
