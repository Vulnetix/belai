# Headless structured output

**Status:** new.

`belai -prompt` runs one prompt without a terminal and prints the reply. This
page covers the contract a script can rely on: a machine-readable result or
event stream, an answer checked against a JSON schema, a prompt read from
standard input, and a stored session continued with a new prompt.

Nothing here changes what the run is allowed to do. Permissions, the sandbox,
the firewall, the classifier, hooks and the rule that an ask nobody can answer
is a deny all apply as before, and `-answers` still answers questions from a
file. Only how the run reports, and what it reads, changes.

- `-output-format text|json|stream-json` chooses how the run reports. The
  default is `text`, the reply and nothing else.
- `-json-schema FILE` requires the final answer to be JSON that satisfies the
  schema in FILE.
- `-prompt -` reads the prompt from standard input. So does a piped or
  redirected standard input with no `-prompt`.
- `-resume ID` and `-continue` now work with `-prompt`: the stored
  conversation is replayed and the new prompt is added to the same transcript.
- Code: `internal/headless/output.go` (events and the result),
  `schema.go` (the schema subset and the check), `stdin.go` (the prompt),
  `resume.go` (replay), `cmd/belai/headlessout.go` (the flags, the repair turn
  and the exit codes), `internal/config/headless.go` (settings).

## Settings

The `headless` block is read from your own settings layers only. A project
file cannot set it, so a repository cannot change what a script that runs
Belai receives or raise the caps. A project value is ignored with a note.

| Key | Meaning | Default |
| --- | --- | --- |
| `headless.output_format` | Default of `-output-format`: `text`, `json` or `stream-json`. The flag wins. | `text` |
| `headless.max_stdin_bytes` | Most bytes of a prompt read from standard input, 1024 to 16777216. | 1048576 (1 MiB) |
| `headless.event_text_cap` | Most bytes of text one event carries, 256 to 1048576. | 4096 (4 KiB) |

A value outside its range fails the settings load and names the key.

## Output formats

`text` prints the reply followed by a newline. The reply is cleaned of control,
bidirectional and delimiter characters, and a value held by the Pix secrets
vault is replaced by `[vault:NAME]`. With `-json-schema`, `text` prints the
validated JSON value instead of the model's wording around it.

`json` prints one JSON object on one line when the run ends: the result.

`stream-json` prints one JSON object per line: an `init` line, events as they
happen, and the result as the last line. Standard output carries only these
lines. Banners, notices and errors go to standard error.

### Events

The event types are a closed set. An agent event that is not mapped here is
dropped, and reasoning, permission asks and clarify asks are never emitted.

| Type | Fields | When |
| --- | --- | --- |
| `init` | `session_id`, `provider`, `model` | First line. |
| `text` | `text` | The model's reply, in whole lines. Text longer than the cap is split into several events. |
| `tool_start` | `id`, `name`, and `args` with `-verbose` | A tool call begins. `args` is capped and flagged with `truncated`. |
| `tool_result` | `id`, `name`, `text`, `truncated` | A tool call ends. `text` is what the model was given, after sanitising and the classifier, so a withheld result stays a placeholder. It is cut at the cap. |
| `warning` | `text` | A non-fatal problem, as one line. |
| `result` | see below | Last line. |

`id` and `name` are reduced to letters, digits, dot, underscore and hyphen.
Every other string is cleaned with the same text sanitiser as a transcript and
encoded as JSON without HTML escaping, so a newline, a quote or text that looks
like an event cannot start a second line. Text is emitted in whole lines so the
delimiter cleaning never sees half of a tag; a partial line is sent when a tool
starts, a warning arrives or the run ends.

### The result

```json
{"type":"result","subtype":"success","is_error":false,"session_id":"...","mode":"agent","passes":1,"reply":"...","structured":null,"usage":{"calls":3,"total_tokens":4821,"prompt_tokens":4400,"completion_tokens":421,"cache_read_tokens":0,"cache_write_tokens":0},"duration_ms":5120}
```

| Field | Meaning |
| --- | --- |
| `subtype` | `success`, `error`, `run_cap`, `schema_failure` or `denied`. |
| `is_error` | True for every subtype but `success`. |
| `session_id` | The session, resumable with `-resume`. |
| `mode`, `passes`, `stop_reason` | What ran. Present when known. |
| `reply` | The model's final text. |
| `structured` | The validated, cleaned JSON value with `-json-schema`, otherwise `null`. |
| `schema_error` | `{"pointer","code"}` when the answer failed the schema. |
| `asks_withheld` | Names of tools whose calls needed an approval nobody could give. |
| `usage` | Token totals from the same summary `-usage-json` writes. |
| `duration_ms` | Wall time of the run. |
| `error` | A cleaned one-line reason for a failure. |

A run that fails before the model is reached (no credentials, a settings
error) still ends with a result of subtype `error` in a structured format.

`reply` and `structured` are model text. Treat them as untrusted data: do not
run them, put them in a shell command or render them as markup without the
care you would give any user input.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | The run finished. |
| 1 | The run failed: an error, a refused prompt, no credentials, a failed resume. |
| 2 | Usage: a bad flag value, an unusable schema, a stdin prompt that is empty, too large or not valid text, or a flag combination the contract cannot honour. |
| 3 | A run cap was reached (`run_caps`, see [Run caps](run-caps.md)). |
| 4 | A tool call needed an approval nobody could give. Structured formats only. |
| 5 | The final answer did not satisfy the schema after one repair. |

Exit code 3 predates this page and keeps its meaning, so the schema failure
code is 5. A plain `text` run without a schema has always exited 0 when asks
were withheld, and still does. Under `json` or `stream-json` the same run exits
4 and lists the tools in `asks_withheld`.

Precedence when more than one applies: error, run cap, schema failure, denied.

## Schema-checked answers

`-json-schema FILE` takes a JSON file of at most 64 KiB. The schema language is
a closed subset of JSON Schema:

| Keyword | Rule |
| --- | --- |
| `type` | `object`, `array`, `string`, `number`, `integer`, `boolean` or `null`. One type, not a list. |
| `properties` | Names are plain text of at most 128 characters. |
| `required` | Each name must be a declared property. |
| `enum` | A non-empty list of JSON values. |
| `items` | One schema for every element. |
| `additionalProperties` | `true` or `false`, not a schema. |
| `description` | Shown to the model, capped at 300 characters. |
| `$schema`, `title` | Accepted and ignored. They never reach the model. |

Any other keyword (`pattern`, `minimum`, `oneOf`, `$ref`, `format`, and the
rest) is an error at load, exit 2, so a schema never promises a check the
harness does not run. Depth is limited to 16 levels and 1024 schema nodes.

How it runs:

1. The model is told the form of its final answer in a sealed directive on the
   user turn. The schema in it is the parsed schema written back out, so the
   model sees the rules that are enforced and nothing else.
2. When the run ends, the reply is checked in plain Go: the reply itself, or the
   body of a reply that is one fenced block, must be a single JSON value with no
   trailing data that satisfies the schema. No model is asked whether the output
   is valid or safe.
3. On failure the run gets one repair turn. Its message is composed by the
   harness: a fixed sentence, one failure code and a JSON pointer built from the
   schema's own property names and array indexes, plus the schema. It never
   contains text from the reply, not even an invented property name.
4. If the repaired answer also fails, the result has subtype `schema_failure`,
   `structured` is `null`, `schema_error` names the code and pointer, and the
   exit code is 5.

The failure codes are `not_json`, `trailing_data`, `too_deep`, `wrong_type`,
`missing_required`, `not_in_enum` and `unexpected_property`. A valid value is
re-encoded with every string and key cleaned, so `structured` holds no control,
bidirectional or delimiter text.

`-json-schema` runs the agent, so it cannot be combined with `-detect-mode`,
`-tools=false` or plan mode (a plan run's deliverable is the plan file). These
are usage errors, exit 2.

## Prompt from standard input

`-prompt -` reads the prompt from standard input. With no `-prompt` at all, a
standard input that is a pipe or a regular file is read the same way, when
Belai is not running interactively. A terminal, `/dev/null` and a socket are
never read, so an inherited descriptor does not make a bare `belai` wait.

The read stops at `headless.max_stdin_bytes` plus one byte. An empty prompt, one
over the limit, one that is not valid UTF-8 and one holding a NUL byte are
usage errors (exit 2) before any model call. The text is then cleaned with the
text sanitiser. It is the user's own prompt, so it takes the same admission as
`-prompt` and a typed line, and is not classified separately.

```
git diff | belai -output-format json -json-schema review.json -prompt -
```

## Resuming with a prompt

`-resume ID` (a full id or a unique prefix) and `-continue` (the newest session
of this project) used to need the terminal UI. With `-prompt` they now load the
stored conversation and run the new prompt as its next turn.

- The conversation is rebuilt from the transcript: prompts, replies, and the
  tool calls that have a stored result, with those results. A call without a
  result and a result without a call are dropped, subagent and render-only rows
  are skipped, and a compaction summary stands in for what came before it.
  Goal, plan and todo state are not carried over; the prompt starts a new turn.
- The stored history is replayed as it was admitted and is not classified
  again. The new prompt is admitted like any other.
- New entries are appended to the same transcript, chained from its last entry,
  so the session keeps one root. The run caps counters are keyed by the session
  id, so spend carries over.
- A session of another project is copied into this one under a new id and the
  copy is continued, as the terminal UI does. The other project's directory is
  never entered, so a resume cannot widen the session's roots, and the trust
  gate for this directory is the only one that applies. A headless run in an
  untrusted directory still fails closed unless `-trust-dir` is given.
- With `-no-transcript` the history is read and nothing is written, not even
  the copy of another project's session.
- An unknown or ambiguous id exits 1 with the candidates listed, and nothing
  reaches the model. `-resume` and `-continue` cannot be combined with
  `-detect-mode` or `-tools=false` (exit 2).

## What is guaranteed

- Standard output holds only the chosen format.
- Every string in an event or result passes the text sanitiser, or the line or
  identifier sanitiser for names, and then the vault scrub. The only secrets
  scrubbed beyond that are the vault's; a value a tool printed is not otherwise
  recognised, so the tool gates (sanitising and the classifier) are what keep
  such a result out.
- A tool result in the stream is the post-gate text. A result the classifier
  withheld is the withheld placeholder.
- The caps and the format default are the user's, never a repository's.
- The result is written even when the run fails, in a structured format.

See [Sanitisation](sanitization.md) for the sink and [Run caps](run-caps.md) for
exit code 3.
