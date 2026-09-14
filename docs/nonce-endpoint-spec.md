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
| `https://guardrails.vulnetix.com/openai/acme/v1` | `https://guardrails.vulnetix.com/openai/acme/v1/nonces` |
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

## Unsupported / not enabled

When a provider does not implement the endpoint, or has it disabled, it returns
`401` (or, for gateways that prefer it, `403`/`404`). Signet treats any of
these as "unsupported" and falls back to its local CSPRNG pool.

## Verification semantics

Nonces fetched from this endpoint are added to the harness's nonce pool. The
delimiter engine then accepts a block only when its nonce is currently reserved
in the pool; a nonce that is merely *available* (fetched but never reserved) is
not accepted. Rotating the pool invalidates all previously issued nonces.