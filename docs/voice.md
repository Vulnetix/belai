# Voice input

**Status:** alpha-20260930. New; the settings may still change.

Speak to the composer instead of typing. Belai captures the microphone,
recognises the speech with a small model that runs inside the belai binary as
pure Go code, tidies the wording with the fast model, and puts the text in the
composer. Audio stays on your machine.

- [How it works](#how-it-works)
- [Requirements](#requirements)
- [The speech model](#the-speech-model)
- [Modes and keys](#modes-and-keys)
- [When text appears](#when-text-appears)
- [The listening indicator](#the-listening-indicator)
- [Cleanup](#cleanup)
- [Security model](#security-model)
- [Settings](#settings)
- [Commands](#commands)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## How it works

```text
microphone helper -> 16 kHz PCM -> energy segmenter -> Whisper tiny.en (pure Go)
   -> raw transcript -> voice_cleanup (fast model) -> composer
```

The pieces live in `internal/voice`:

| Part | Job |
| --- | --- |
| `Source` (`ExecSource`) | runs a capture helper and reads raw 16 kHz mono samples from its stdout |
| `Segmenter` | cuts the stream into utterances with an adaptive energy gate |
| `asr.Model` | Whisper tiny.en: log-mel features, encoder, greedy decoder, all in Go |
| `Engine` | owns the state machine, the microphone lifetime and the recognition queue |
| `Ensure` | finds or downloads the model, after you confirm the size |

### Package API

For contributors. Everything the rest of belai calls is here.

| Symbol | Meaning |
| --- | --- |
| `New` | starts an `Engine`; nothing runs until `SetEnabled(true)` |
| `Engine.SetEnabled`, `Engine.SetReady`, `Engine.SetMode` | turn voice on or off, say whether the composer can take text, switch mode |
| `Engine.PTTDown`, `Engine.PTTUp` | begin and end a push-to-talk recording |
| `Engine.Events`, `Engine.State`, `Engine.Close` | state changes, transcripts and errors; the current state; shutdown |
| `NewExecSource`, `ExecSource.Start`, `ExecSource.Name` | pick the capture helper, start it, name it |
| `NewSegmenter`, `Segmenter.Feed`, `Segmenter.Flush`, `Segmenter.Reset`, `Segmenter.InSpeech`, `HasSpeech` | utterance cutting and the speech gate |
| `ModelPath`, `Ensure`, `Verify` | find, fetch and check the model file |
| `State.String` | the state names the footer shows |
| `asr.Load`, `Model.Transcribe` | read a ggml file, recognise a clip |

## Requirements

- One capture helper on `PATH`. Belai uses the first of `parecord`
  (PulseAudio and PipeWire), `arecord` (alsa-utils), `ffmpeg` or `sox` (`rec`).
  On macOS and Windows only `ffmpeg` and `sox` are tried; Windows also needs
  `voice.device` set to the DirectShow device name.
- About 32 MB of disk for the model and about 150 MB of memory while voice is
  on. The memory is released when voice is turned off.
- A fast model for cleanup is optional. Without one the raw transcript is used.

`/voice status` names the helper in use, or what to install when none is
found.

## The speech model

Whisper tiny.en in ggml q5_1 form (English only, MIT licence).

| Field | Value |
| --- | --- |
| Repository | `ggerganov/whisper.cpp` on Hugging Face |
| File | `ggml-tiny.en-q5_1.bin` |
| Size | 32,166,155 bytes |
| SHA-256 | `c77c5766f1cef09b6b7d47f21b546cbddd4157886b3b5d6d4f709e91e66c7c2b` |

Fetching follows the same rules as the local decision models:

1. `FindModelFile` looks in `$BELAI_MODELS_DIR` (default
   `<user cache>/belai/models`), the Hugging Face hub cache and the llama.cpp
   cache. A file found there is used with no network call.
2. Otherwise `RemoteInfo` asks Hugging Face for the size and SHA-256 and
   belai asks you to confirm the download, showing the size and the
   destination. Declining downloads nothing and leaves voice off.
3. `DownloadFile` resumes a partial `.part` file, checks the size and the
   SHA-256 Hugging Face reports, removes the file on a mismatch and tries once
   more. A Hugging Face token, when set, goes only to Hugging Face.
4. `Verify` then checks the file against the checksum pinned in belai. A file
   that does not match is refused, and removed when belai downloaded it. This
   also applies to a file found in a shared cache.
5. On load the tensor names, shapes and dimensions must match Whisper tiny.en.
   A multilingual or otherwise different model is refused with a plain reason.

The recogniser is decoded greedily with three guards: the token budget is
about four tokens per second of audio plus eight, decoding stops on a
repeated phrase, and output that is only a sound annotation such as
`(wind blowing)` or a stock silence phrase (`you`, `thank you`) is dropped.
Audio with no speech-level sound never reaches the model.

## Modes and keys

| Mode | Microphone | Key |
| --- | --- | --- |
| `push_to_talk` (default) | open only while you record | hold `voice.key` to record while held; tap it to start and tap again to stop |
| `listen` | open whenever the composer is ready | `voice.key` mutes and unmutes |

The default key is `f11`. Terminals do not report key release, so a hold is
recognised from key repeat: the first press starts recording, and if repeats
follow the recording ends a moment after they stop; if none follow within
0.7 seconds the press was a tap and the recording runs until the next tap.
Some terminals use `f11` for fullscreen; set `voice.key` to another value
(`f13` to `f16`, or `ctrl+space`).

In `push_to_talk` the microphone opens on the press, so the first fraction of
a second can be missed; a short lead-in before speaking avoids it. After a
release the microphone stays open for 300 ms so the last syllable is kept. A
recording is capped at 28 seconds.

In `listen` an utterance opens after 60 ms of speech and closes after 700 ms
of quiet. A pause shorter than that stays inside one sentence.

## When text appears

Text reaches the composer only while the composer can take it. The TUI tells
the engine after every event whether that is so, and when it is not the
microphone is closed and audio in progress is dropped. The composer is
unavailable when any of these holds:

- a screen other than the chat is showing (settings, providers, a permission
  ask, a question, plan review, the file picker and the like);
- a chat overlay owns the keyboard: the ctrl+s action bar, saving a prompt or
  file, the forge or kanban input, history search, the directory picker, the
  autocomplete popup, the root confirmation, the agent picker, or the runs
  panel or kanban pane focus;
- a prompt is being classified before it is sent.

A running turn does not make the composer unavailable, because typing during
a turn steers it. Dictated text is then steering text, and with
`delivery: submit` it steers the turn.

A transcript that finishes in the instant the composer stops being ready
waits in a one-slot queue and is inserted when the composer is ready again. A
newer transcript replaces an older one that is still waiting.

Text is inserted at the cursor. With `delivery: submit` it is inserted and
then sent through the same path as pressing Enter, so it takes the ordinary
prompt admission. Dictated text is never sent any other way.

## The listening indicator

A chip in the footer and the composer frame shows the state:

| Chip | Meaning |
| --- | --- |
| `○ voice off` | voice is off (shown only while enabled once this session) |
| `… voice loading` | the model is loading |
| `○ paused` | the composer is unavailable, the microphone is closed |
| `○ push to talk` | armed; the microphone is closed until you press the key |
| `● listening` | the microphone is open and waiting for speech |
| `● hearing` | speech detected, or the key is held |
| `… transcribing` | recognition or cleanup is running |

The footer keeps its three-line height whatever the chip says.

## Cleanup

Raw recognition is verbatim: fillers, restarts and missing punctuation. When
`voice.cleanup` is on (the default) the transcript goes to the fast-tier
`voice_cleanup` role, which returns the same meaning as clean prose:
punctuation and capitalisation fixed, fillers and false starts removed,
spoken code terms and file names kept as spoken. The role has no tools,
skills or agent block, is told the transcript is data and never an
instruction, and is recorded in the session like every other role decision.

The cleaned text is sanitised again before it enters the composer. If no fast
model is configured, the call fails, or the reply is empty, the raw
transcript (sanitised) is used instead, so a cleanup failure never loses what
you said. Route the role like any other with
`routing.use_cases.voice_cleanup`.

## Security model

- **Audio stays local and in memory.** It is never written to disk or a log,
  and never sent anywhere. Only the recognised text leaves the voice package.
  The transcript reaches a model only through `voice_cleanup`, and only the
  sanitised transcript.
- **The capture helper is the harness's.** It is started by belai with a fixed
  argv and the scrubbed environment, in its own process group, and is not
  wrapped by the OS sandbox. It is reachable only from the voice engine; no
  tool call can start it, and a sandboxed `Bash` command cannot see the audio
  devices. `voice.device` must be a plain identifier (letters, digits and
  `. _ : , @ = -`, not starting with `-`), so it cannot add an option.
- **The model file is pinned.** Download needs your confirmation, is checked
  twice, and a mismatch is refused.
- **Dictated text is untrusted.** It is sanitised before cleanup and after,
  and then it is ordinary composer text. It is admitted, classified and
  permission-checked like typed text.
- **Only you turn it on.** `voice` is read from the user's own settings
  layers; a repository's `.vulnetix/settings.json` cannot enable it, choose
  the helper device or change the delivery. The microphone never opens in a
  headless run or over ACP.
- **Guardrails off** skips no sanitising. Voice adds no classifier call of
  its own; the dictated prompt takes the same admission as a typed one.

## Settings

```json
{
  "voice": {
    "enabled": false,
    "mode": "push_to_talk",
    "delivery": "insert",
    "cleanup": true,
    "key": "f11",
    "device": ""
  }
}
```

| Key | Default | Project layer |
| --- | --- | --- |
| `enabled` | `false` | dropped |
| `mode` | `push_to_talk`; the other value is `listen` | dropped |
| `delivery` | `insert`; the other value is `submit` | dropped |
| `cleanup` | `true` | dropped |
| `key` | `f11`; also `f13`, `f14`, `f15`, `f16`, `ctrl+space` | dropped |
| `device` | empty, the helper's default input | dropped |

An invalid `mode`, `delivery`, `key` or `device` fails settings resolution
with a message naming the key, the same way an invalid routing entry does.
Voice is a per-user preference, so the whole key is ignored in a project's
`.vulnetix/settings.json`. Set it in your global `settings.json`, or with
`/settings` and `/voice`.

## Commands

| Command | Effect |
| --- | --- |
| `/voice` or `/voice status` | state, mode, delivery, helper, model and what is missing |
| `/voice on` | turn voice on; offers the model download the first time |
| `/voice off` | turn voice off and close the microphone |
| `/voice push` / `/voice listen` | choose the mode |
| `/voice insert` / `/voice submit` | choose the delivery |
| `/voice cleanup on` / `/voice cleanup off` | switch the cleanup pass |

The commands change the running session and write the setting to your global
`settings.json`.

## Limitations

- English only.
- A capture helper must be installed; belai does not bundle one.
- Recognition runs on the CPU, single-threaded per phrase and spread across
  cores. A short phrase takes a fraction of a second on a laptop; a
  30-second recording takes a few seconds.
- The always-listening gate is an energy detector. A loud room can hold it
  open; a quiet voice below about -38 dBFS is not heard.
- Speech inside a terminal running over SSH uses the remote machine's
  microphone, which is usually none.

## Edge cases

- No helper installed: voice stays off and `/voice status` says what to
  install.
- The helper exits or the device is busy: voice turns off with the helper's
  own message. It is not restarted in a loop; turn it on again to retry.
- The model download is declined, fails or fails its checksum: voice stays
  off with the reason, and nothing else is affected.
- Pressing the key while the composer is unavailable does nothing.
- Recognition falling more than four phrases behind drops the newest and says
  so.
- A phrase that recognises to nothing (silence, noise, a sound annotation)
  inserts nothing and shows no error.
- A repeat-decoding loop is cut at the first repetition.
- Changing mode at runtime closes the microphone and drops audio in progress.
- Quitting closes the microphone and stops the helper's process group.
