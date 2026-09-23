# Nonce endpoint spec (designed forward)

Signet seals every harness-generated delimiter with a random nonce plus a
SHA-256 integrity hash of the enclosed content. By default Signet mints its own
nonces from a CSPRNG pool. Providers (or the ai-firewall gateway in front of
them) may instead supply the nonces, which the harness then verifies rather
than mints.

## Endpoint

```
GET {base_url}/v1/nonces
```

`{base_url}` is the provider/gateway base URL without the surface version
prefix. OpenAI-style base URLs already end in `/v1`; the trailing `/v1` is
normalised so the endpoint is always exactly `/v1/nonces` (never
`/v1/v1/nonces`). Anthropic-style base URLs carry no `/v1`, so the endpoint is
appended directly.

Examples:

| Base URL                                   | Nonce endpoint                                            |
| ------------------------------------------ | --------------------------------------------------------- |
| `https://api.openai.com/v1`                | `https://api.openai.com/v1/nonces`                        |
| `https://api.anthropic.com`                | `https://api.anthropic.com/v1/nonces`                     |

## Request

The request carries the same credentials as the provider surface. When an API
key is supplied, Signet sends it as a Bearer token:

```
Authorization: Bearer <api_key>
```

## Response (200 OK)

A JSON object with a numbered list of nonces:

```json
{
  "nonces": [
    "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
  ],
  "count": 2
}
```

- `nonces` — array of nonce strings (opaque, hex-encoded random values).
- `count` — number of nonces in the list.

`nonces` is authoritative: `count` is decoded but never enforced, so a
response whose `count` disagrees with the array length is accepted and the
array wins. A `200` carrying an empty `nonces` array is also accepted as-is —
it seeds nothing and does **not** fall back to local minting, because the
provider answered successfully and claiming otherwise would be a guess.

Fetched nonces are *appended* to the pool's available list rather than
replacing it, so seeding from a provider never discards nonces the pool
already holds. Only `Rotate` discards.

Every request carries Signet's `user-agent`
(`signet/<version> (+https://github.com/Vulnetix/signet)`). Provider turns also
carry the `X-Signet-Session-Id`, `X-Signet-Client-Version`,
`X-Signet-Client-Build` and W3C `traceparent` headers described in
[architecture.md](architecture.md#outbound-identification-and-trace-headers).
A provider may log them for correlation but must not depend on them.

## Unsupported / not enabled

When a provider does not implement the endpoint, or has it disabled, it returns
`401` (or, for gateways that prefer it, `403`/`404`). Signet treats any of
these as "unsupported" and falls back to its local CSPRNG pool.

The unsupported answer is **negative-cached per base URL for the process
lifetime**: a provider without the endpoint is probed at most once, so
repeated session construction and pool invalidation never re-hit the
401/403/404. Only the three "absent" statuses poison the cache — a `200`
response, any other status (5xx et al.), and transport errors including the
3-second timeout are all re-tried next time.

Unsupported is the **only** answer that falls back to local minting. Any
other failure — a 5xx, a transport error, the 3-second timeout, or a `200`
whose body is not valid JSON — propagates to the caller and fails pool
seeding rather than silently substituting local nonces. The distinction is
deliberate: "this provider does not offer nonces" is a known state to degrade
from, while "the nonce provider is broken right now" is not, and quietly
minting locally would hide a gateway outage from an operator who chose to
have nonces supplied.

Subagents skip the GET entirely. They discard the provider-seeded pool for a
fresh local one immediately after construction, so they seed locally and the
round trip is never made.

## Verification semantics

Nonces fetched from this endpoint are added to the harness's nonce pool. The
delimiter engine then accepts a block only when its nonce is currently reserved
in the pool; a nonce that is merely *available* (fetched but never reserved) is
not accepted. Rotating the pool invalidates all previously issued nonces.