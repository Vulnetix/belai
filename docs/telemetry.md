# OpenTelemetry export

**Status:** Roadmap. This feature is designed but not yet in a release, so the
page describes the planned behaviour.

Signet can send traces and metrics to an OpenTelemetry collector over OTLP, so
teams can see turn latency, token use, tool calls and classifier verdicts in
the observability stack they already run. It carries facts about the session,
never its content.

- [What is exported](#what-is-exported)
- [What is never exported](#what-is-never-exported)
- [Settings](#settings)
- [Relationship to SIGNET_TRACE](#relationship-to-signet_trace)

## What is exported

Spans:

| Span | Attributes |
| --- | --- |
| `signet.turn` | mode, pass count, outcome |
| `signet.model_call` | provider, model, role, input and output tokens, cache tokens |
| `signet.tool_call` | tool name, tool kind, permission decision, outcome |
| `signet.classify` | role, verdict sentinel |
| `signet.hook` | hook name, event, decision |
| `signet.mcp_call` | server name, tool name, outcome |

Spans share the trace id Signet already sends in `traceparent` headers and
passes to child processes as `TRACEPARENT`, so a gateway or tool that also
reports to the collector joins the same trace.

Metrics:

- `signet.tokens` (counter): by provider, model, role and direction.
- `signet.tool_calls` (counter): by tool kind and decision.
- `signet.classifier_verdicts` (counter): by role and verdict.
- `signet.turn.duration` (histogram): by mode.

Resource attributes: `service.name=signet`, `service.version`, and a hashed
project key.

## What is never exported

Prompts, replies, reasoning, tool arguments, tool output, file paths, file
contents, commands and URLs. The exporter builds attributes from a fixed
allowlist of keys, and a test fails if any other key is emitted.

## Settings

```json
{
  "telemetry": {
    "otlp_endpoint": "http://localhost:4318",
    "headers": {"x-api-key": "credential:otel"},
    "traces": true,
    "metrics": true
  }
}
```

The standard `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS` and
`OTEL_SDK_DISABLED` variables are honoured. The transport is OTLP over HTTP
with JSON encoding.

The project layer cannot set `telemetry` at all. A repository choosing where
your session facts are sent would be an exfiltration path, so `resolve.go`
drops the whole key from project settings.

Export is batched on a background goroutine. If the collector is slow or down,
data is dropped rather than slowing a turn.

## Relationship to SIGNET_TRACE

`SIGNET_TRACE=<file>` keeps working and writes the local JSONL timing trace
described in [development](development.md). OpenTelemetry export is separate
and can be on at the same time.
