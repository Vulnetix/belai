# OpenTelemetry export

**Status:** alpha-20260926. Shipped in an early form; span and metric names
may still change.

Signet can send traces and metrics to an OpenTelemetry collector over OTLP, so
teams can see turn latency, token use, tool calls and classifier decisions in
the observability stack they already run. It carries facts about the session,
never its content.

- [What is exported](#what-is-exported)
- [What is never exported](#what-is-never-exported)
- [Settings](#settings)
- [Relationship to the local trace](#relationship-to-the-local-trace)
- [Edge cases](#edge-cases)

## What is exported

Spans:

| Span | Attributes |
| --- | --- |
| `signet.turn` | `signet.mode`, `signet.outcome` (`ok`, `cancelled`, `refused`, `hook_blocked`, `error`), `signet.passes` |
| `signet.tool_call` | `signet.tool.name`, `signet.tool.kind`, `signet.outcome` (`ok`, `denied`, `hook_denied`, `classified_unsafe`, `withheld`, `rejected`) |

Both use the session's trace id, the same one Signet already sends in
`traceparent` headers and passes to child processes as `TRACEPARENT`, so a
gateway or tool that also reports to the collector joins the same trace.

Metrics (cumulative):

| Metric | Type | Attributes |
| --- | --- | --- |
| `signet.tokens` | counter | `signet.provider`, `signet.model`, `signet.tokens.estimated` |
| `signet.model_calls` | counter | `signet.provider`, `signet.model` |
| `signet.tool_calls` | counter | `signet.tool.kind`, `signet.outcome` |
| `signet.role_decisions` | counter | `signet.role` (the role-manager decision, such as `security_sentinel` or `mode_classify`), `signet.verdict` |
| `signet.hook_runs`, `signet.hook_failures` | counter | `signet.hook.event`, `signet.decision` |
| `signet.turn.duration` | histogram, seconds | `signet.mode`, `signet.outcome` |

Resource attributes: `service.name=signet`, `service.version`, and
`signet.project`, a hash of the project path.

Export covers the TUI, background agents, headless `-prompt` runs and
`signet acp`.

## What is never exported

Prompts, replies, reasoning, tool arguments, tool output, file paths, file
contents, commands and URLs. Two rules in `internal/otel` enforce it: an
attribute whose key is not on a fixed allowlist is dropped, and every string
value is reduced to identifier characters (letters, digits and `._:/@+-`) and
capped at 96 characters. Tests fail if a key outside the allowlist appears,
and a test runs a real turn and checks that none of its prompt, arguments,
file contents or reply reach the collector.

## Settings

```json
{
  "telemetry": {
    "otlp_endpoint": "http://localhost:4318",
    "headers": {"x-api-key": "env:OTEL_API_KEY"},
    "traces": true,
    "metrics": true
  }
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `otlp_endpoint` | none (export off) | collector base URL |
| `headers` | none | sent with every export |
| `traces` | `true` | export spans |
| `metrics` | `true` | export metrics |

A header value `env:NAME` is read from the environment. The standard
`OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS` and
`OTEL_SDK_DISABLED` variables are honoured. The transport is OTLP over HTTP
with JSON encoding, posted to `<endpoint>/v1/traces` and
`<endpoint>/v1/metrics`.

The project layer cannot set `telemetry` at all. A repository choosing where
your session facts are sent would be an exfiltration path, so `resolve.go`
drops the whole key from project settings.

Export is batched on a background goroutine every 10 seconds and flushed when
Signet exits. If the collector is slow or down, data is dropped rather than
slowing a turn, and at most 4096 spans wait between exports.

## Relationship to the local trace

`SIGNET_TRACE=<file>` keeps working and writes the local JSONL timing trace
described in [development](development.md). OpenTelemetry export is separate
and can be on at the same time.

## Edge cases

- `OTEL_SDK_DISABLED=true` turns export off whatever the settings say.
- `OTEL_EXPORTER_OTLP_ENDPOINT` overrides `otlp_endpoint`.
- A trailing `/` on the endpoint is removed.
- With `traces` and `metrics` both false, nothing starts.
- A string attribute such as a model id keeps letters, digits and `._:/@+-`;
  anything else becomes `_`, and it is cut at 96 characters.
- A collector that is down or slow loses that batch; the next export
  carries the counters again, because they are cumulative.
