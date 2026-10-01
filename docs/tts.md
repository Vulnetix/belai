# Read aloud

**Status:** alpha-20260930. New; the settings may still change.

Belai can read a reply to you. A turn's final reply is read when it ends (if you
ask for that), any reply can be read with `ctrl+b`, saying "stop" stops it, and
a card in the thread shows a player with the position, a scrub bar, speed
buttons and level bars. The audio is kept in a local cache, so a replay costs
nothing and works offline.

- [What leaves your machine](#what-leaves-your-machine)
- [Turning it on](#turning-it-on)
- [What is read](#what-is-read)
- [Playing: ctrl+b, /tts and the card](#playing-ctrlb-tts-and-the-card)
- [The player card](#the-player-card)
- [Saying stop](#saying-stop)
- [The cache](#the-cache)
- [How it works](#how-it-works)
- [Package API](#package-api)
- [Settings](#settings)
- [Commands](#commands)
- [Security model](#security-model)
- [Limitations](#limitations)
- [Edge cases](#edge-cases)

## What leaves your machine

There is no speech model small enough to run in a pure Go binary today, so the
engine is **Microsoft's read-aloud service**, the endpoint the Edge browser uses
(`speech.platform.bing.com`). The text you have read aloud is sent to it, over
an encrypted websocket, and the audio comes back. It is free and needs no key,
and it is unofficial: it can change, rate-limit or stop working without notice.

Because text leaves the machine, reading aloud is **off until you turn it on**
with `/tts on`, and that command is also your agreement. Nothing is sent before
it, a repository cannot turn it on, and the speech-to-text side is unaffected:
your microphone audio never leaves the machine.

The engine sits behind an interface (`Engine`), so a local model can replace it
later without touching the player, the card or the cache.

## Turning it on

`/tts on` prints what is sent and where, sets `tts.enabled` and records that you
agreed (`tts.consented`). `/tts off` turns it off and stops playback. The
`read aloud` row in `/settings` can turn an agreed feature off and on again; on
a fresh install it does not turn it on, and says to run `/tts on` instead, so a
toggle is never the moment text starts going off the machine. Reading aloud also
needs an audio player program installed (see [How it works](#how-it-works));
`/tts on` tells you if none is found, and nothing is requested from the service
until one is.

`tts.read_reports` (the `read reports aloud` row, or `/tts reports on`) reads
each turn's final reply when the turn ends. It does nothing while `tts.enabled`
is off.

## What is read

The text of one message, prepared by `Prepare` so it sounds like prose:

| In the message | Read as |
| --- | --- |
| a fenced code block (closed or not) | "Code omitted." |
| a markdown link | its text, without the address |
| a bare URL | "link" |
| an image | nothing |
| a table | one sentence per row, cells separated by commas; the separator row is dropped |
| a heading, list item or table row | a sentence of its own, with a full stop added if it has none |
| bold, italics, `inline code`, quotes, rules, HTML tags | the words only |
| control characters | dropped |

Sentences are packed into pieces of about 800 characters (`GroupChars`), so the
first piece is short and playback starts quickly; a sentence longer than that is
split at a word. A message is read up to 12,000 characters (`MaxChars`), cut at a
sentence. A message with nothing readable says so and sends nothing. A sentence
is not split at an abbreviation such as "e.g." when the next word is lower case.

The text is sanitised (`sanitize.Text`) before anything is prepared, and only
the prepared text leaves the machine.

## Playing: ctrl+b, /tts and the card

`ctrl+b` is used the way `ctrl+c` copies: over a reply in the transcript it reads
that reply; otherwise it reads the last final reply. Pressed on the reply that is
being read it pauses or resumes; pressed on another reply it switches to that
one. With reading aloud off it says how to turn it on. While the pointer is over
a reply and it is on, the footer hint offers `ctrl+b read aloud`. `ctrl+b` is
also tmux's default prefix, so tmux users press it twice.

Reading starts a new card in the thread. Only the current card answers clicks:
when another reply is read, the earlier card is left as it was, marked stopped
and "replaced".

## The player card

```text
read aloud · The build passed and I pushed…            playing · 1×
  ◂◂ 10s  ❚❚ pause  ■ stop  10s ▸▸   speed 0.75× 1× [1.25×] 1.5× 2×
  1:12 ━━━━━━━━━●──────────────── 3:40+
  ▁▃▅▇▅▃▂▄▆▅▃▁▂▄▅▃▂▁▂▃▅▃▂▁  synthesising 2 of 5
```

- **Transport.** `◂◂ 10s` and `10s ▸▸` move ten seconds; the centre button is
  play, pause, `↻ replay` when the clip ended or was stopped, or a spinner while
  it waits for audio; `■` stops. On a narrow terminal the words drop and only
  the glyphs remain.
- **Speed chips.** 0.75, 1, 1.25, 1.5 and 2. The pitch is kept (a
  waveform-similarity overlap-add, `Stretch`). The default comes from
  `tts.speed`; a click changes the one clip.
- **Scrub bar.** The position and the audio available so far, with a `+` while
  more is still being synthesised. Click anywhere on it to seek, or press and
  drag. While audio is arriving the bar spans what has arrived.
- **Level bars.** The loudness of the last 24 readings (about 1.2 seconds) of what was played, measured
  from the audio itself, so they move with the voice. They are flat when not
  playing.
- **Status.** `synthesising n of m`, `from cache`, `done · cached for replay`,
  or the error.

The card is animated every 100 ms while something moves and stops animating when
playback is finished or paused. It is render-only: the message holds a few
numbers and no audio, is marked ephemeral, and never reaches the session record,
sync or a model. After a resume the old card is gone; `ctrl+b` on the reply
makes a new one, from the cache.

## Saying stop

While something is being read, the voice keyword "stop" stops the audio, before
it would interrupt a turn (see [voice input](voice.md#spoken-keywords)). It needs
voice input on; in `listen` mode the microphone stays open for it while the
composer is busy, and like every spoken keyword it is the whole utterance only.

The speaker reaches the microphone, and there is no echo cancellation, so two
things keep the reply from being taken for you:

- a transcript of two or more words where at least six in ten are words being
  read is dropped unheard (playing, or in the three seconds after). A single
  word is never echo, so "stop" always gets through;
- the words are compared, not matched as a phrase, so a different reading of
  the same sentence is still caught.

Headphones are the reliable answer. A `/tts stop` or the stop button does the
same without the microphone. Stopping keeps the clip: play starts again from
the top.

## The cache

Audio is cached as raw 24 kHz 16-bit mono PCM under your user cache directory
(`belai/tts`), so a message read before replays at once, with no network and
without a new request. Seek and speed changes come from the cached audio.

- The key is a SHA-256 of the engine, the voice and the prepared text, so a
  different voice or text is a different clip, and a file name never contains
  what was read.
- The directory is private (mode 0700), files are written whole through a
  `.part` name, and a key that is not a SHA-256 hex string is refused.
- The size is capped by `tts.cache_mb` (default 256). Past it the least
  recently used clips go first; a clip larger than the whole cap is not stored.
  `0` turns the cache off: nothing is written, every read requests again.
- A message is stored only when it was read to the end. An interrupted or failed
  synthesis is not cached.
- `/tts cache` shows the size and `/tts cache clear` removes every clip, and
  only clips.
- This is the one place audio touches disk. Microphone audio never does.

## How it works

```text
message -> Prepare -> pieces -> Synthesize (up to 4 at once) -> PCM -> cache
                                     |                              |
                                     v                              v
                          Player (paced writes) -> helper program -> speaker
```

- **Synthesis.** `Synthesize` runs up to four requests at once (`Workers`) and
  delivers the pieces in order, so the first can play while the rest are made. A
  cached message is one piece. A failed piece ends the stream with its error;
  what arrived still plays, then the card shows the error.
- **The service.** One websocket per piece to a fixed host, https only. The service offers MP3 and WebM/Opus, not raw audio, so each piece is asked for as 24 kHz mono MP3 and decoded in process with a pure-Go decoder (`go-mp3`, so releases stay free of cgo). The bytes come off the network, so the decoder runs as untrusted-input code: a panic becomes an error, the decoded size is bounded, and anything that is not audio is refused. A sample rate other than 24 kHz is resampled, not guessed at. The
  request carries a token computed from the time (`SecMSGEC`: the time rounded
  down to five minutes, hashed with the public client token). A 403 that carries a `Date` header is taken for a clock that is off: the
  header corrects the clock and the request is tried again at once, without
  waiting, and a 403 that keeps coming ends the request after the same number of
  attempts as any other failure. A 401, or a 403 with no `Date`, is not retried.
  Other failures are retried up to three times, waiting 1, 2 and 4 seconds (a
  wait never exceeds 8). A reply with no audio is an error.
- **Voices.** `tts.voice` is a plain name such as `en-US-AndrewMultilingualNeural`
  (the default), `en-US-AriaNeural` or `en-GB-RyanNeural`. Anything but letters,
  digits and `-` is refused, and the text is escaped before it goes into the
  request, so neither can add markup.
- **The player.** `Player` writes 50 ms of audio at a time to a helper program
  and paces itself, about 150 ms ahead of the clock, so the position is exact
  and pause, seek and speed are instant: pausing kills the helper, and a seek or
  speed change starts a new one at the new place. It waits, showing a spinner,
  when it catches up with audio still being made.
- **The helper program.** The first found of: on Linux `paplay`, `aplay`,
  `ffplay`, `play` (sox); on macOS `ffplay`, `play`; on Windows `ffplay`. Each is
  started with a fixed argument list (raw 24 kHz 16-bit mono on stdin), the
  scrubbed environment and its own process group, outside the OS sandbox, and is
  never reachable from a tool call. `NewExecOpener` finds it; none installed is
  a clear error and no request.

## Package API

`internal/tts` is the engine, the cache and the player, with no TUI in it:

| Name | What it does |
| --- | --- |
| `Engine` | turns one prepared piece of text into PCM; `Edge` is the only one |
| `NewEdge`, `Edge.Name`, `Edge.Synthesize` | the read-aloud engine, with retries and clock correction |
| `SecMSGEC` | the service's time-based token |
| `ValidVoice` | whether a name is a plain voice name |
| `Prepare` | markdown to the pieces that are read |
| `Synthesize` | runs the pieces through an engine and the cache, in order |
| `Key`, `DefaultCacheDir`, `NewCache`, `Cache.Get`, `Cache.Put`, `Cache.Size`, `Cache.Clear` | the audio cache |
| `Stretch` | time-stretch that keeps the pitch (speed 0.5 to 3) |
| `NewPlayer`, `Player.Append`, `Player.Finish`, `Player.Play`, `Player.Pause`, `Player.Toggle`, `Player.Stop`, `Player.Seek`, `Player.SetSpeed`, `Player.Close`, `Player.Snapshot` | the player |
| `State.String`, `State.Active` | the player's states: idle, buffering, playing, paused, stopped, ended, failed |
| `NewExecOpener` | finds the playback helper program |

## Settings

```json
{
  "tts": {
    "enabled": false,
    "consented": false,
    "read_reports": false,
    "voice": "en-US-AndrewMultilingualNeural",
    "speed": 1,
    "cache_mb": 256
  }
}
```

| Key | Default | Project layer |
| --- | --- | --- |
| `enabled` | `false`; only takes effect once `consented` is true | dropped |
| `consented` | `false`; written by `/tts on` | dropped |
| `read_reports` | `false`; read each turn's final reply when it ends | dropped |
| `voice` | `en-US-AndrewMultilingualNeural`; letters, digits and `-`, up to 64 | dropped |
| `speed` | `1`; from 0.5 to 3 | dropped |
| `cache_mb` | `256`; from 0 to 4096, `0` turns the cache off | dropped |

An invalid `voice`, `speed` or `cache_mb` fails settings resolution with a
message naming the key. Reading aloud sends text off the machine, so the whole
`tts` key is ignored in a project's `.vulnetix/settings.json`: set it in your
global `settings.json`, or with `/settings` and `/tts`.

`/settings` has five rows, all written to the global file: `read aloud`
(`enabled`), `read reports aloud`, `read aloud voice`, `read aloud speed` and
`read aloud cache`. `x` returns a row to its default. Turning `read aloud` off
stops playback; changing the speed applies to the clip that is playing.

## Commands

| Command | Effect |
| --- | --- |
| `/tts` or `/tts status` | state, reports, voice, speed and cache size |
| `/tts on` | agree to send text to the service and turn reading aloud on |
| `/tts off` | turn it off and stop playback |
| `/tts stop` | stop what is playing; play starts again from the top |
| `/tts reports on` / `/tts reports off` | read each final reply when the turn ends |
| `/tts voice NAME` | set the voice; an invalid name is refused |
| `/tts speed N` | set the speed, 0.5 to 3; applies to the playing clip |
| `/tts cache` / `/tts cache clear` | show the cache size, or empty it |

`ctrl+b` reads, pauses and resumes.

## Security model

- **Off by default, and agreed to.** Nothing is requested before `/tts on`. The
  setting is per-user: the project layer is dropped, so a repository cannot turn
  it on, pick a voice or size the cache.
- **One host.** Text goes only to `speech.platform.bing.com`, over `wss`, through
  the same endpoint rules as any service URL (https, or loopback in tests). No
  redirect is followed: any answer but an upgrade is an error. The only
  credential is a public constant in the request, never sent anywhere else, and
  no user credential or API key is involved. It is not routed through an AI
  Firewall.
- **Text in, nothing else.** The prepared, sanitised text of one message. Code
  blocks are not read and never sent. The voice name is validated and the text
  escaped for the request.
- **Never recorded.** The text read, the audio and the card do not enter
  telemetry, a notification, the session record, sync or a model's transcript.
  The card message is ephemeral and holds no audio.
- **No model-facing tool.** A model cannot start playback, in the same way it
  cannot start the microphone.
- **The helper runs scrubbed.** A fixed argument list, the scrubbed environment
  and its own process group, outside the OS sandbox because it is harness-fixed
  and receives only audio.
- **The cache.** Under the user's cache directory, private, hashed names, capped,
  and the only place audio is written to disk.

## Limitations

- English voices are the tested ones; other voices the service offers should
  work by name.
- The service is unofficial and needs the network for a message not yet cached. It checks the browser version it is told about and has refused old ones (HTTP 403), so belai can stop reading aloud when the service moves on. `go test -tags live -run TestLiveEdge ./internal/tts` sends one fixed phrase ("Hello from belai.") and says whether the protocol still works.
- A player program must be installed (`paplay`, `aplay`, `ffplay` or sox `play`;
  macOS `afplay` needs a file, so it is not used).
- There is no echo cancellation: use headphones if "stop" must work over the
  speaker. The echo filter handles the speaker reading your reply back, not
  other sound.
- The position trails the sound by a few hundred milliseconds of device buffer,
  which the card subtracts on a fixed estimate.
- Reading aloud is for the interactive terminal. It does nothing in a headless
  run or over ACP.

## Edge cases

- **Audio that does not decode.** A reply that is not MP3, is truncated or is corrupt becomes an error on the card and is never cached; it cannot crash belai.
- **Nothing to read.** A message that is only code reads "Code omitted."; one
  with nothing at all says so and sends nothing.
- **Reading while reading.** A new read replaces the current one; its card is
  frozen and the old synthesis is cancelled.
- **The service closes the stream.** A close frame with a code and reason (for example an unsupported format) is shown as the error, not as a bare end of stream.
- **Closed or failed.** A failed piece plays what arrived, then shows the error
  on the card; a failure before any audio is an error at once. No player
  program: an error, no request.
- **A seek past the audio made so far** clamps to the end of what exists and
  waits for more.
- **Pause** stops the sound at once and resumes from where it was heard, not
  from where the writes reached.
- **Replay** of a clip that ended, or was stopped, starts from the top; a seek
  on a finished clip waits, paused, at the new place.
- **Turn off mid-play.** `/tts off`, or the settings row, stops the sound and
  drops the clip.
- **No cache directory.** The cache is off for that run; reading still works.
- **A short clip** is played as it is; very short audio is resampled rather than
  stretched.
