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
- [How the words arrive](#how-the-words-arrive)
- [Typing cancels voice](#typing-cancels-voice)
- [The wake word](#the-wake-word)
- [Spoken keywords](#spoken-keywords)
- [Spoken instructions](#spoken-instructions)
- [The listening indicator](#the-listening-indicator)
- [In the transcript](#in-the-transcript)
- [Diagnosing with /voice debug](#diagnosing-with-voice-debug)
- [Cleanup](#cleanup)
- [Security model](#security-model)
- [Settings](#settings)
- [Commands](#commands)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## How it works

```text
microphone helper -> 16 kHz PCM -> energy segmenter -> Whisper tiny.en (pure Go)
   -> running guess -> composer, live, while you speak
   -> raw transcript -> composer at once -> voice_cleanup streams over it
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
| `Engine.PTTDown`, `Engine.PTTLatch`, `Engine.PTTUp` | begin a push-to-talk recording, turn it into a tap that ends when the speaker stops, end it |
| `Engine.Cancel` | drop everything in progress at once: the recording, and any result still on its way from the recogniser |
| `Engine.Level`, `Engine.Samples`, `Level`, `Silent`, `SpeechDB` | how loud the microphone is, in dBFS; how many samples have arrived at all; the level treated as speech |
| `Engine.Events`, `Engine.State`, `Engine.Done`, `Engine.Close` | state changes, transcripts and errors; the current state; closed when the engine stops; shutdown |
| `NewExecSource`, `ExecSource.Start`, `ExecSource.Name`, `ExecSource.Command` | pick the capture helper, start it, name it, print its command line |
| `Devices` | list the inputs the sound system offers, with fixed programs and cleaned, capped output |
| `NewSegmenter`, `Segmenter.Feed`, `Segmenter.Flush`, `Segmenter.Reset`, `Segmenter.InSpeech`, `Segmenter.Snapshot`, `HasSpeech` | utterance cutting, a copy of the utterance so far, and the speech gate |
| `ModelPath`, `Ensure`, `Verify` | find, fetch and check the model file |
| `Embedded`, `ModelSource`, `LoadModel` | whether the model is built in, where it comes from, and reading it (the embedded copy, else the file), checked against the pinned SHA-256 |
| `State.String` | the state names the footer shows |
| `asr.Load`, `asr.LoadBytes`, `Model.Transcribe` | read a ggml file or bytes already in memory, recognise a clip |

## Requirements

- One capture helper on `PATH`. Belai uses the first of `parecord`
  (PulseAudio and PipeWire), `arecord` (alsa-utils), `ffmpeg` or `sox` (`rec`).
  On macOS and Windows only `ffmpeg` and `sox` are tried; Windows also needs
  `voice.device` set to the DirectShow device name.
- About 150 MB of memory while voice is on, released when voice is turned off.
  A build without the model inside also needs 32 MB of disk for it.
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

### Built into release binaries

Every release family except the plain `belai` build carries the model inside
the binary (`belai-no-classifier`, `belai-bert-guardrails`, which Homebrew,
Scoop and `install.sh` install by default, and `belai-bert-guardrails-jailbreak`),
the same way the classifier models are embedded: `just voiceprep` (which the
build recipes for those variants run) fetches the file once into the `assets`
directory of `internal/voice`, which is gitignored, and the `belai_voice` build
tag embeds it with `go:embed`. Those binaries therefore need no download and no
network to dictate. `Embedded` reports it, `LoadModel` reads it, and
`/voice status` says `model: built in`.

The embedded copy is hashed against the pinned SHA-256 each time it loads.
`tools/voiceprep` verifies the file it fetched, and CI's `voice` job runs the
package tests and a build with the tag, so a checksum that no longer matches
Hugging Face fails before a release. Release builds cache the file under a key
taken from `internal/voice/models.go`, where the checksum is pinned.

The plain `belai` release build is the small one (about 24 MB against about 56 MB
for `belai-no-classifier`) and embeds nothing, and neither does a `go build`
made without the tag. They use the download path below: voice is off until you
run `/voice on`, then `/voice download` fetches the 32 MB file once.

### Fetching it for a build without the model inside

The same rules as the local decision models apply:

1. `FindModelFile` looks in `$BELAI_MODELS_DIR` (default
   `<user cache>/belai/models`), the Hugging Face hub cache and the llama.cpp
   cache. A file found there is used with no network call.
2. Otherwise belai downloads nothing on its own. Turning voice on (`/voice on`,
   the setting, or the `/settings` row) prints the file name, the size and
   the destination and stops. Running `/voice download` is the confirmation:
   `Ensure` asks Hugging Face (`RemoteInfo`) for the size and SHA-256 and
   fetches the file. Once it arrives, voice starts if you had asked for it.
   Without the command, voice stays off and nothing else is affected.
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
| `push_to_talk` (default) | open only while you record | hold `voice.key` to record while held; tap it to record until you stop speaking |
| `listen` | open whenever the composer is ready | `voice.key` mutes and unmutes |

Voice is on by default where the model is built into the binary (every release
family except the plain `belai` build); a build without the model stays off
until `/voice on`. Install a
capture helper, then hold `f11` and speak. Turn voice off with `/voice off`,
which is remembered. Pressing the key while voice is not running is not
silent: belai says once per session why (voice is off, the helper is missing,
the model is not downloaded) and what to run. A default start with no capture
helper prints nothing at launch, so a machine without a microphone is not
told on every run.

The default key is `f11`. Some terminals keep it for fullscreen and never
send it, so `voice.key` can be `ctrl+space`, `ctrl+]` or `ctrl+g`, which need
no special keyboard, or `f13` to `f16` on a keyboard that has them. Choose
one in `/settings` (the `voice key` row) or in `settings.json`, and check it
reaches belai with `/voice debug`.

Terminals do not report key release, so belai reads the key another way. The
first press starts recording. If key repeat follows, it is a hold: the
recording ends a moment after the repeats stop. If no repeat follows within
0.7 seconds it was a tap: the recording continues without the key and ends by
itself once you have spoken and then been quiet for a second, or after ten
seconds with no speech at all. A second tap ends it at once. So a missed
release can never leave the microphone open.

In `push_to_talk` the microphone opens on the press, so the first fraction of
a second can be missed; a short lead-in before speaking avoids it. After a
release the microphone stays open for 300 ms so the last syllable is kept. A
recording is capped at 28 seconds.

In `listen` an utterance opens after 60 ms of speech and closes after one second
of quiet. A pause shorter than that stays inside one sentence; a longer one ends the utterance, and the next phrase is recognised on its own, so a very short fragment after a long pause can come out empty. `push_to_talk` records the whole phrase and has no such cut.

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

A transcript that finishes in the instant the composer stops being ready is
held and inserted when the composer is ready again. Transcripts held together
are joined in the order they were spoken, up to 4,000 characters (the oldest
words are dropped past that). Transcripts waiting for the cleanup pass are
handled one at a time, in order, and at most eight wait.

Text is inserted at the cursor, with a space added when the text before the
cursor does not end in whitespace. With `delivery: submit` it is inserted and
then sent through the same path as pressing Enter, so it takes the ordinary
prompt admission (with no agent engaged that path opens the agent picker, as
Enter does). Dictated text is never sent any other way.

Submit never fires when the composer would then hold text that runs
something: a line that starts with `/` (a command), a line that starts with
`!` (a shell command), or an `@path` (a file attachment). The text is still
inserted, belai says why it was not sent, and you press Enter yourself.

## How the words arrive

The words appear as they are said, not after the last of them. Three steps
write the same stretch of the composer, the live span:

1. **A running guess while you speak.** Every 1.2 seconds of speech the engine
   recognises everything heard so far again and sends the result as a partial.
   The composer shows it at the cursor and replaces it with each better guess.
   A guess needs at least 0.6 seconds of speech, is never made while a final is
   being recognised, and is cancelled if the final arrives first.
2. **The recognised text, at once.** When the phrase ends (the key is
   released, the tap stops itself, or the pause closes it) the final
   transcript replaces the guess in the composer immediately, before the fast
   model has said anything.
3. **The tidy-up, streamed over it.** The fast model's reply streams in, and
   each piece replaces the raw words in the composer with the cleaned text so
   far. If the reply is empty, runs away or fails, the raw words are put back.

With `delivery: submit` the composer is sent only once step 3 has finished, so
what is sent is the tidied text. A guess never replaces text you edited: if
you change the live stretch it becomes yours and voice stops writing to it.
Phrases that end while an earlier one is still being tidied wait their turn
and keep their order. If the composer cannot take text (see the list above)
the phrase is tidied out of sight and held until it can.

## Typing cancels voice

Any key you type in the composer cancels everything voice is doing, at once:

- a recording in progress ends and its audio is dropped (push to talk closes
  the microphone; always-listening keeps it open for the next phrase);
- a guess or a tidy-up in flight is cut off and anything it still sends is
  ignored;
- phrases waiting for their turn, and text held for a busy composer, are
  discarded;
- the Enter that `delivery: submit` would press is not pressed.

Your own Enter cancels voice too, then sends as usual. What voice already put
in the composer stays; it is yours to keep or delete, and a prompt that still
holds it is still tagged as dictated. Only the voice key does not cancel: it
is voice. A key pressed on another screen does nothing to voice, and typing
while nothing is happening (a quietly listening microphone) leaves it alone.
The cancel takes effect before the engine has processed anything else, so a
result that was already being recognised cannot land late.

## The wake word

With `voice.wake_word` on, belai acts only on speech that starts with "Hey,
Belay". The microphone stays open in `listen` mode, but everything else is
dropped the moment it is recognised: it is never shown, never written to the
composer, never sent to the tidy-up model or Jev, and never recorded. There is
no running guess either, because a guess is raw text.

- The phrase is stripped first. "Hey, Belay, fix the failing test" goes on as
  "fix the failing test", and that is all any later step sees.
- The opening word can be "hey", or what a recogniser makes of it: "hay", "hi",
  "a", "okay" or "ok". The name tolerates the usual misspellings (one letter off
  "belay" or "belai", with an optional trailing "s", between five and eight
  letters, and "bellay" or "belly"), and only at the very start of the
  utterance. "They said
  hey belay" and "hey there belay" do not match.
- A bare "Hey, Belay" arms the next utterance for eight seconds, so you can
  pause and then say the instruction. One utterance uses it up.
- Turning the wake word on sets `voice.mode` to `listen`, because push to talk
  is not listening. Settings that set `wake_word` without `listen` fail
  validation, naming the key.

The wake word is a filter on what you dictate, not authentication: anyone in
earshot can say it. Every action it leads to still goes through the same
permission rules, asks and sandbox as if you had typed it.

## Spoken keywords

With `voice.commands` on (the default), a few words said on their own act
without any model. The match is on the whole utterance after punctuation and a
leading or trailing "please" are removed, so "stop the server" is ordinary
speech and "stop" is not.

| Say | When | Does |
| --- | --- | --- |
| stop, cancel, interrupt | a reply is being read aloud ([read aloud](tts.md#saying-stop)) | stops the audio, and nothing else; it takes priority over the next row |
| stop, cancel, interrupt | a turn is running, no ask open | interrupts it, as Esc does |
| approve, approved, allow, yes | a permission ask is open | allow once |
| approve always, always approve, always allow, allow always, approved always | a permission ask is open | allow and save the rule; only these exact phrases |
| deny, denied, reject, no | a permission ask is open | deny |
| option 1 to option 4 (the word before the number can be option, number, choice, select, choose or pick; "2" alone works too, and the recogniser's homophones won, to, too and for count as 1, 2, 2 and 4) | a clarification is open | picks that option in the first group with no answer |
| submit, send, done | a clarification is open | sends the answers shown, as Enter does |
| skip | a clarification is open | skips the first group with no answer |
| approve, deny | the plan review is open | approve here, or keep the plan without executing it |

A keyword with nothing to apply to is ordinary speech and is dictated like any
other words. An option number the group does not have is refused. A single
choice group that becomes answered last sends the answers at once, so "option
2" alone is select and proceed; a multiple choice group only toggles, and
"submit" sends. A keyword never answers an ask other than the one open when it
was heard, and each one writes a `voice: ...` line in the transcript and records
its source as `voice`.

While one of those asks is on screen the composer is not, so dictation is
closed. In `listen` mode the microphone stays open for these keywords alone:
anything else said is dropped there, never shown, and never sent anywhere.
Push to talk has no open microphone to keep, so answering an ask by voice needs `listen`.
With the wake word on, "Hey, Belay, deny" works as well as a bare "deny".

## Spoken instructions

A short utterance (up to 24 words) that is not a keyword is offered to Jev's
`voice_command` job ([Jev jobs](jev-jobs.md#voice-command)). Jev rates it
against what you could mean: your skills, saved prompts and processes, agent
profiles and crews, the security review, plan mode and goal mode. Exactly one
must rate at or above `jev.thresholds.voice_at` (0.95 by default); two close
matches, a score just under, no backend or a failure leaves the speech as
ordinary dictation. Only the speech and the target names reach the backend.

| Target | What running it does |
| --- | --- |
| skill | submits "use the NAME skill" through the typed-prompt path |
| security review | starts `/vulnetix review` |
| crew | starts the crew, as `/fleet crew NAME` |
| agent profile | switches to agent mode with that profile, as `/agent:NAME` |
| process | starts the saved process, as `/process:NAME`; plan mode still refuses |
| prompt | puts the saved prompt in the composer for you to send, as `/prompt:NAME` |
| plan, goal | switches the sticky mode, as `/mode` |

The job never approves anything. It calls the function the slash command calls,
so that command's own checks hold, and it runs only what is saved in your
libraries, never a command line you said. Typing while a match is pending
cancels it. The role-manager record holds the kind of target and a rounded
score, never the speech or a name.

## The listening indicator

Three things show the state. The `voice:` switch in the footer says it in
words. A microphone sits in the composer: a capsule on a U-shaped holder with
a pole and a base, three rows by nine cells, drawn as a Braille dot bitmap
(the design system allows no emoji). It starts on the first text row and is
flush right. The composer's frame reacts to the voice.
Nothing is drawn while voice is off, and none of it appears on another
screen's text field. The footer keeps its three-line height.

Where the text you typed reaches the mark's columns, the mark gives way and a
small one-cell icon (`◉` `◎` `⊘` `◌`) is drawn at the right end of the first
row instead, so nothing you wrote is ever covered. The empty composer's
placeholder is shorter while voice runs (`Type, or hold f11 and speak…`) so the
right-hand mark has room on an empty composer, and the text keeps two columns
clear at the right for the small icon.

| State | Footer | Composer mark | Meaning |
| --- | --- | --- | --- |
| downloading | `○ voice: downloading` | none | the model is being fetched |
| loading | `○ voice: loading` | still, outlined microphone | the model is being read into memory |
| paused | `○ voice: paused` | still, outlined microphone | the composer is unavailable and the microphone is closed |
| push to talk | `○ voice: push to talk · f11` | still, outlined microphone | armed; the microphone is closed until you press the key |
| muted | `○ voice: muted` | outlined microphone with a slash through it | listen mode, silenced with the key |
| listening | `● voice: listening` | solid microphone pulsing to a dithered one | the microphone is open and waiting for speech |
| hearing | `● voice: hearing` | solid microphone with one, then two sound arcs each side, quickly | speech is being heard |
| transcribing | `● voice: transcribing` | purple microphone alternating solid and soft | recognition or the fast model is working |

The pulse and the arcs change shape as well as colour, so they show on a terminal without
colour.

The state follows the voice, not the key: with the key held (or a tap
recording) it reads `listening` until speech arrives, `hearing` while you
speak and again `listening` in the pauses, then `transcribing`.

The composer frame carries the same signal. While speech is heard its top and
bottom rules ripple slowly, drawn with scan-line glyphs at staggered heights,
and stay still otherwise. While the speech model or the fast model is working
the whole frame turns a pastel purple (`ColorVoice`), and returns to teal when
the text lands. The mark and the ripple advance on a 140 ms timer that runs
only while something is moving.

## In the transcript

A prompt that still holds dictated text is a voice turn. Its `you` title is a
pastel purple (`ColorVoice`); a prompt you typed is titled in pink
(`ColorYou`). The title bar of a voice turn says `ctrl+o raw`, and `ctrl+o`
shows what the speech model recognised, under the tidied text, as
`raw · ...`. A turn where the fast model changed nothing shows no raw line. A
prompt mixing typed and dictated text is a voice turn, with only the dictated
parts in its raw text.

## Diagnosing with /voice debug

`/voice debug` opens a screen for "I talked and nothing came out". It pauses
the normal engine, listens with an engine of its own, and brings the normal
one back when you press `esc` or `q`. `c` clears the log. It shows, top to
bottom:

| Section | What it tells you |
| --- | --- |
| Hardware | the capture helper and its exact command line, the default input and the inputs PulseAudio or PipeWire report (or ALSA's cards), whether the model is built in or on disk, and the engine's state or the reason it cannot start |
| Microphone level | a live meter in dBFS with the speech level marked, the RMS and peak numbers and a short history. After three seconds it says which of three things is true: no sound reached belai at all (*perhaps the microphone is on mute*, or the wrong input is selected), sound arrives but below the speech level (speak closer, raise the gain), or the microphone is working |
| Raw transcript | the last 400 characters the speech model recognised, exactly as recognised |
| Fast-model cleanup | that raw text tidied by the fast model, live: it updates as you keep speaking, keeps the last result up while a new one is made, and says plainly when there is no fast model or the call failed |
| Events | a timestamped log of the keys the terminal delivered, the engine's states, transcripts, cleanups and errors |

The event log is how to verify keys: a press of the voice key logs `press`;
if the terminal repeats a held key it logs the first repeat with its delay
(so a hold works), and when the repeats stop it logs `release inferred` with
the repeat count and duration; a press with no repeat logs `that was a tap`.
Any other key is logged as not the voice key. No repeat and no tap line means
the terminal never sent the key, which is when to pick another `voice.key`.

The screen writes nothing to disk. Its hardware probes run `pactl` and
`arecord` with fixed arguments and the scrubbed environment, capped and
cleaned. The raw text goes to the fast model only through `voice_cleanup`,
like a normal dictation, and never to a log.

## Cleanup

Raw recognition is verbatim: fillers, restarts and missing punctuation. When
`voice.cleanup` is on (the default) the transcript goes to the fast-tier
`voice_cleanup` role, which returns the same meaning as clean prose:
punctuation and capitalisation fixed, fillers and false starts removed,
spoken code terms and file names kept as spoken. The role has no tools,
skills or agent block, is told the transcript is data and never an
instruction, and is recorded in the session like every other role decision.

The role is called over the streaming transport, so the cleaned text arrives
piece by piece and replaces the raw words in the composer as it is written
(see [How the words arrive](#how-the-words-arrive)). Every piece is sanitised
before it reaches the composer. A reply that grows past twice the transcript
plus 40 bytes is cut off mid-stream. If no fast model is configured, the call
fails, or the reply is empty or runs away, the raw transcript stays, so a
cleanup failure never loses what you said. Route the role like any other with
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
  `. _ : , @ = -`, at most 128, not starting with `-` or `=`), so it cannot add an
  option. It is
  given no file to write (`parecord` reads a lone `-` as a file name, not as
  standard output, so it gets none), and it runs in a private temporary
  directory that is removed when it ends, so a mistake could never leave a
  recording in your project.
- **The model file is pinned.** Download needs your confirmation, is checked
  twice, and a mismatch is refused.
- **Dictated text is untrusted.** It is sanitised before cleanup and after,
  and then it is ordinary composer text. It is admitted, classified and
  permission-checked like typed text.
- **Only you choose it.** `voice` is read from the user's own settings
  layers; a repository's `.vulnetix/settings.json` cannot enable it, choose
  the helper device or change the delivery. The microphone never opens in a
  headless run or over ACP.
- **On by default only where it is safe to be.** Voice starts on its own only
  when the speech model is built into the binary, and its default mode is push
  to talk, where the microphone is open only while you hold the key or during a
  tap recording (which ends by itself). `voice.enabled: false` turns it off for
  good, and `always listening` is a mode you choose.
- **`/voice debug` listens too, and says so.** It opens the microphone in its
  own engine while it is on screen and closes it when you leave, including if
  another key takes the screen away. Its probes run fixed programs (`pactl`,
  `arecord`) with the scrubbed environment and no argument from a setting or a
  model, and it writes nothing to disk.
- **Guardrails off** skips no sanitising. Voice adds no classifier call of
  its own; the dictated prompt takes the same admission as a typed one.

## Settings

```json
{
  "voice": {
    "enabled": true,
    "mode": "push_to_talk",
    "delivery": "insert",
    "cleanup": true,
    "log": true,
    "key": "f11",
    "device": "",
    "wake_word": false,
    "commands": true
  }
}
```

| Key | Default | Project layer |
| --- | --- | --- |
| `enabled` | on when the model is built into the binary (every release family except plain `belai`), otherwise `false`; an explicit `false` always wins | dropped |
| `mode` | `push_to_talk`; the other value is `listen` | dropped |
| `delivery` | `insert`; the other value is `submit` | dropped |
| `cleanup` | `true` | dropped |
| `log` | `true`; `false` hides voice's automatic notices and its cleanup rows from the transcript | dropped |
| `key` | `f11`; also `ctrl+space`, `ctrl+]`, `ctrl+g`, and `f13`, `f14`, `f15`, `f16` on a keyboard that has them | dropped |
| `device` | empty, the helper's default input | dropped |
| `wake_word` | `false`; `true` acts only on speech that starts with "Hey, Belay" and needs `mode` `listen` | dropped |
| `commands` | `true`; `false` turns off the spoken keywords and the `voice_command` job | dropped |

An invalid `mode`, `delivery`, `key` or `device`, or a `wake_word` without
`listen`, fails settings resolution
with a message naming the key, the same way an invalid routing entry does.
Voice is a per-user preference, so the whole key is ignored in a project's
`.vulnetix/settings.json`. Set it in your global `settings.json`, or with
`/settings` and `/voice`.

`/settings` has eight rows, all written to the global file: `voice input`
(`enabled`), `voice mode`, `voice delivery`, `voice cleanup`, `wake word`, `voice commands`, `voice log` and `voice key`. Turning
`voice input` on or off, or changing the mode, takes effect at once; delivery
and cleanup apply from the next transcript. `x` returns a row to its default.
`device` is set in the file.

### The voice log

`voice.log` (the `voice log` row) decides whether the transcript shows voice's
own automatic notices. With it on (the default) the thread shows a voice error
(a capture helper that stopped, a failed recognition), the note that a
dictation was inserted but not sent, and the `voice_cleanup` role-manager row
for each tidy-up, at whatever detail `ui.show_internal_work` allows. With it
off none of those is drawn. What you asked for is always shown: `/voice status`
and the other command answers, and the one hint the voice key gives when voice
is not running. The switch changes what the thread shows and nothing else: the
session record still holds every role-manager decision, and
`/voice debug` keeps its own event log either way.

## Commands

| Command | Effect |
| --- | --- |
| `/voice` or `/voice status` | two lines: state, mode, delivery, cleanup, key, wake word and commands; then the capture helper and whether the model is built in or on disk |
| `/voice debug` | open the diagnostic screen (see above) |
| `/voice on` | turn voice on and remember it; with no model it shows the offer and waits |
| `/voice off` | turn voice off, remember it and close the microphone |
| `/voice download` | fetch the speech model, after you have seen its size and destination |
| `/voice push` / `/voice listen` | choose the mode |
| `/voice insert` / `/voice submit` | choose the delivery |
| `/voice cleanup on` / `/voice cleanup off` | switch the cleanup pass |
| `/voice wake on` / `/voice wake off` | switch the wake word; on also sets the mode to `listen` |
| `/voice commands on` / `/voice commands off` | switch the spoken keywords and instructions |

Each command that changes a setting writes it to your global `settings.json`
and applies to the running session. Any other argument prints the usage line.

## Limitations

- English only.
- A capture helper must be installed; belai does not bundle one.
- Recognition runs on the CPU, spread across the cores. A phrase of two to
  five seconds takes a fraction of a second on a laptop; a 30-second
  recording takes a few seconds. Longer audio is recognised in 30-second
  windows.
- The always-listening gate is an energy detector. A loud room can hold it
  open; a quiet voice below about -38 dBFS is not heard.
- Speech inside a terminal running over SSH uses the remote machine's
  microphone, which is usually none.
- Voice runs only in the interactive TUI, never in headless runs or over ACP.

## Edge cases

- No helper installed: voice stays off and `/voice status` says what to
  install.
- The helper exits or the device is busy: voice turns off with the helper's
  own message. It is not restarted in a loop; turn it on again to retry.
- The model is missing: `/voice on` shows the offer and voice stays off until
  `/voice download` finishes. A failed or checksum-failing download says why,
  and nothing else is affected.
- Pressing the key while voice is off shows one hint per session (voice is off,
  what to run, and that a terminal which keeps the key for itself needs another
  `voice.key`). With voice on, pressing it while the composer is unavailable
  does nothing.
- Losing the composer while the key is held ends that recording without
  recognising it.
- Recognition falling more than four phrases behind drops the newest and says
  so.
- A phrase that recognises to nothing (silence, noise, a sound annotation)
  inserts nothing and shows no error.
- A repeat-decoding loop is cut at the first repetition.
- A failed, empty or runaway cleanup inserts the raw transcript.
- Dictation that would start with `/` or `!`, or holds an `@path`, is inserted
  but never sent by `delivery: submit`.
- Changing mode at runtime closes the microphone and drops audio in progress.
- Turning voice off drops held and queued dictation.
- Quitting closes the microphone and stops the helper's process group.
- Typing while voice is recording, writing or about to send cancels it at
  once, including the Enter that submit delivery would press; the text
  already in the composer stays.
- A guess that arrives after the final for the same phrase, or after a cancel,
  is dropped, never shown.
- A tidy-up that fails halfway puts the raw words back over the partly cleaned
  ones.
- A phrase that ends while the composer is unavailable is tidied out of sight
  and held, not streamed.
