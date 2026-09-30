# Images

Two things share this page: images a **tool returns** (built), and images the
user **attaches** with `@` (deferred).

## Tool-returned images (built)

A harness-owned capture tool (`Screenshot`, see [screenshots](screenshots.md))
can return pixels beside its text. The text classifier reads text and cannot
judge an image, so pixels take a separate, deterministic path.

- **Only `KindScreenshot` carries pixels.** `tools.Result.Images` is honoured
  for a `KindScreenshot` result and dropped for every other kind, so an MCP
  server, a hook or a subagent cannot use it to smuggle bytes into a turn.
  The kind is mutating (the capture observes the desktop), sanitise-only for
  its text, and never in `classifierKinds`;
  `TestClassifierKindsIsExactlyTheArbitraryContentSet` records why.
- **Admission is `internal/imageguard`, and nothing else.** The header must
  decode as PNG or JPEG (GIF, WebP and anything else are refused). The
  declared size is checked against a pixel budget (50 million) from the header
  alone, so a small file that declares a huge canvas is refused before any
  pixel is allocated. The input is capped at 32 MiB (a 4K desktop PNG is large before it is reduced). The pixels are decoded,
  reduced to a long edge of 1568 pixels by averaging (which keeps small text
  legible), flattened onto white and re-encoded as a new PNG. Metadata
  chunks, trailing bytes and polyglot payloads do not survive because the
  output is rebuilt from pixels. Any failure refuses the image; there is no
  partial admission. The refusal note is harness text with the decoder's
  reason cleaned and capped, and the refused bytes are never echoed.
- **A withheld result carries no image.** If the text of the result was
  withheld, the image is dropped with it. A call with nowhere to put an image
  (a subagent) drops it and says so.
- **Only the newest image is sent.** Older tool images in the history are
  replaced by a one-line harness note, so a long session does not pay for every
  screenshot it ever took. The conversation itself is never edited: the
  request builder works on a copy (`run.prepareToolImages`).
- **A model without image input is told.** `models.Vision` decides from the
  model id (Claude 3 and later, GPT-4o, 4.1, 5 and o3/o4-mini, Gemini, and a
  few others; an id it does not name is text-only). A text-only model gets a
  harness note instead of the image. Kiro decides from its live catalogue
  (`kiromodels.Info.Images`) as for an attached image.
- **User images use the same path.** An image on a user turn is shaped by the
  same rules as a tool image: only the newest image in the conversation is
  sent, whichever role it is on (a screenshot taken after an attached image
  replaces it, and the reverse), and a model without image input gets a
  harness note on that turn instead. The model accepts images when the custom
  provider profile declares `"images": true` for it, or, without a
  declaration, when `models.Vision` names its id. Text attachments on the
  same turn are kept when the images are dropped.
- **Token estimate.** An image counts as its area divided by 750 pixels,
  at least 85 and at most 1600 tokens, in the request-shape and per-call
  estimates. Provider-reported usage replaces the estimate when it arrives.
- **Encoding per surface.** Anthropic: the `tool_result` content becomes a
  block list, the text first and then a base64 `image` block. OpenAI-style chat
  completions: a tool message has no image seat, so the image rides on a user
  message of `image_url` parts (a data URI) that follows the whole run of tool
  results answering one assistant turn; a user message in the middle of that
  run would break the tool-call pairing. Kiro: the image joins the current
  user message like an attached one, with the history's images cleared. An
  image the service would refuse (over 3.75 MB, or a format it does not list)
  is named in the message or tool result instead of dropped silently.
  A **user** image needs no follow-up message: on chat completions it is
  `image_url` parts on the user message after its text (or alone, with no
  empty text part), and on Anthropic it is `image` blocks ahead of the text.
  That covers OpenAI, OpenRouter, Gemini's OpenAI-compatible endpoint, Ollama,
  llama-server, Groq, DeepSeek, Mistral, Together, xAI, Moonshot, MiniMax,
  Alibaba, Hugging Face, Cloudflare Workers AI and the AI Gateway (Claude
  models take the Anthropic shape), and custom `openai-chat` and
  `anthropic-messages` profiles. GitHub Copilot additionally gets
  `Copilot-Vision-Request: true` on a request that carries an image, and
  only then. The `openai-responses` surface has no request path in Belai, so
  it has no image path either.
- **What is not covered.** Pixels can still show text. A page or window the
  model is shown may contain words written to look like instructions. The
  result text says the image is data, the `Screenshot` tool asks before a
  desktop capture and only captures loopback pages (see
  [screenshots](screenshots.md)), and an image is never a reason to act
  without the permission rules applying to what the model does next.

## Attached images (deferred)

Images are intentionally **out of scope** for the first `@` file chooser. The
chooser lists only text-bearing files; image extensions are filtered out in
`internal/tui/filepick.go`.

This part records the design work so a later round can pick it up without
re-discovering the constraints.

## Why images are not this round

1. **Cell content is sanitised.** `components.NewSeg`/`sanitiseCells`
   (`internal/tui/components/styledline.go`) strips every ESC/C0/C1 byte from
   file-derived content. A kitty/sixel inline image therefore needs a
   deliberate bypass around the styled-line sanitiser, not a free path through
   the existing `Read` panel.

2. **Segments carry one foreground.** `components.Seg` has a foreground colour
   only, and `components.Row.BG` is per-row. Half-block rendering (the
   stdlib-only option) needs a per-segment background colour so two vertical
   pixels can share one cell. That is a new `Seg` field and a new rendering
   branch in the line painter.

3. **`tools.Read` rejects NUL bytes.** The reader treats a NUL as a binary
   marker (`internal/tools/read.go`). Image bytes are full of NULs, so they
   cannot flow through the current `Read` tool unchanged; either the reader
   gets an image mode or images bypass the text tool entirely.

4. **`run.Attachment` has an `image` kind** (`MediaType`, `Data`) that egress
   keeps out of the text body. The wire shapes now encode it for tool results
   (see above), and the Kiro encoder sends it (see
   [Kiro](kiro.md#models-effort-and-images)). Nothing creates a user-attached
   one yet. When that feature lands it reuses `internal/imageguard` for
   admission, because image bytes cannot be text-classified.

## Candidate designs

### Option A: half-block terminal preview, stdlib only

Decode the image with `image/png`, `image/jpeg`, and `image/gif` from the
standard library, downsample to the panel width, and render each cell as a pair
of pixels using the upper/lower half-block glyph (`▄`/`▀`) with per-segment
foreground and background colours.

Pros:

- No new dependencies.
- Works in every terminal that can display 24-bit colour.
- Falls back gracefully to a summary when the terminal reports fewer than 256
  colours.

Cons:

- Half blocks halve the vertical resolution.
- Requires the `Seg` background and line-painter changes noted above.
- Still cannot send the image to a model until the wire shape is multimodal.

### Option B: kitty/sixel graphics protocol

Write the raw image bytes to the terminal through the kitty or sixel protocol.

Pros:

- Native resolution, no `Seg` changes needed for the terminal path.
- Can support animation and mouse interaction later.

Cons:

- Detection is required (query the terminal for `TERM`/`TERM_PROGRAM` and
  optionally send a kitty graphics protocol query).
- Need a fallback to Option A when the protocol is unsupported.
- `styledline.go` must learn a "raw bytes" segment kind that bypasses
  sanitisation and width measurement.

## Fullscreen viewer

Both options benefit from a fullscreen image view, triggered from the transcript
row. The natural place is a new `viewState` in `internal/tui/view.go`, entered
with a dedicated key while the Read panel is focused, and exited with `esc`.

## Recommended order

1. Add a per-segment background to `components.Seg` and teach the line painter
   to emit half-block cells.
2. Add an `image` attachment kind and a decoder that creates a half-block
   preview.
3. Render the preview in a new transcript row type (`Role: "tool"`,
   `ToolName: "Read"`, plus a `Meta["image"] = true` marker).
4. Gate the feature behind a `settings`/`state` flag until the wire shape is
   multimodal.
5. Extend `run.Attachment` and `internal/wire` for image payloads, then map
   them per provider.
