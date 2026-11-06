# Side questions (`/btw`)

**Status:** new.

`/btw <question>` asks the session's model one thing on the side. The model
answers in a separate call that sees a short summary of the session and no
tools. Neither your question nor the answer joins the conversation, the
session file, a compaction or a handoff. The answer appears in the thread as a
row marked "side answer, not in history" and exists only on your screen.

Use it for a quick factual question while a long turn runs, or between turns,
without paying to carry the exchange in every later request or steering the
agent by accident. It is not steering (text typed during a turn is steering and
does join the turn) and it is not a subagent (a subagent has tools and reports
back into the conversation).

## Using it

```
/btw what does parse.go export?
/btw clear
```

| Command | Effect |
| --- | --- |
| `/btw <question>` | Ask. A row says a side question is being asked; the answer follows as its own row, with the first 200 characters of your question above it. |
| `/btw clear` | Hide every side row from the thread. They were never recorded; this only tidies the screen. |
| `/btw` | Print the usage line. |

It works in the interactive TUI, including while a turn is running. It
refuses, and says why, when `side.enabled` is false and while an earlier side
question is still running. The question is the user's own text, so a model can
never start one: there is no tool for it.

## What the side question reads

The call runs on the session's current model and posture. Its prompt is a fixed
harness lead, your question, and a harness summary built from state the session
already holds:

- the repository line and the files this session changed (only while A2A is on)
  and the todo list, at most 20 items, each cleaned to one line and capped at
  200 characters;
- the last `side.history_turns` user and assistant messages, oldest first, each
  capped at 1500 characters, 8 KiB in all. With `side.history_turns` at 0 the
  summary carries the latest task and reply instead and no other turns.

Tool results, subagent rows, messages from a failed attempt, harness rows and
earlier side answers are never included. The model is told the summary is data,
not instructions.

## What the harness does and what the model does

The harness chooses the window, builds the summary, owns the size cap and
decides what is shown. The model writes prose only. No model is asked whether
anything is safe.

- No tools. The side session is built with an empty registry and the settings
  that add a tool (offload, deferred tools, code mode) switched off, and a test
  pins that the request carries no tool definitions. The repo memory tools are
  not added either, so a side question cannot write a note.
- One round. The call has a two minute timeout and is not retried. Nothing
  falls back into the main conversation.
- Your question and the summary go through the same prompt admission a typed
  prompt does, under the session's posture. A refusal shows the fixed failure
  line.
- The answer is the model's own text for your screen. It is sanitised, an empty
  answer is an error, and it is cut on a character boundary at
  `side.max_reply_bytes`. It is never classified or stored because nothing reads
  it again. If a later change ever fed it to a model it would have to be
  classified first.
- A call that times out or fails shows the failure line below. An answer that
  arrives after you moved to another session (`/clear`, `/new`, `/resume`) is
  dropped.

Any failure shows one fixed line, "btw: the side question could not be
answered, nothing was added to the conversation". Provider text is never put in
the thread.

## What is kept

Nothing. The row is held in memory only (`Ephemeral`, so `persistTail` and the
session mirror skip it), is not part of the turns built for the provider, and
is skipped by the serializer that `/compact` and `/relay` read. The model call
is a usage event like any other: the budget ledger records its tokens under the
role `side`, apart from the agent role, so it never replaces the request the
prompt cache keep-alive replays and is left out of `/context` and the reply cost
line. The security classifier's decisions on the question are role-manager
activity and are recorded as any other (event and verdict, never text).

Hooks: the side session inherits the session's hook set, so `user_prompt_submit`
sees the side prompt (the fixed lead, your question and the summary) and a
`stop` hook fires when the call ends. A `deny` from a prompt hook refuses the
question. Tool hooks never fire because there are no tools.

Run caps do not count a side question as a turn and do not stop one; its
tokens appear in the ledger.

## Settings

| Key | Default | Range | Meaning |
| --- | --- | --- | --- |
| `side.enabled` | on | on or off | Turns `/btw` off. |
| `side.history_turns` | 6 | 0 to 20 | How many recent user and assistant messages the question may read. A value outside the range is clamped. |
| `side.max_reply_bytes` | 4096 | 512 to 8192 | The size cap of a shown answer. A value outside the range is clamped. |

Your own layers set any key. A project settings file, which comes with a cloned
repository, may only turn `/btw` off or lower a limit (a lower value is applied,
an equal or higher one is dropped with a note). It can never enable the command
or raise a limit. The merge is `mergeSide` in `internal/config/side.go`, called
from `Resolve` and `Settings.Override`. `/settings` has a `side questions` row
for the switch in General; the two limits are settings file keys. There is no
flag and no environment variable.
