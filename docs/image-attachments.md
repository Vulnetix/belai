# Image attachments (deferred)

Images are intentionally **out of scope** for the first `@` file chooser. The
chooser lists only text-bearing files; image extensions are filtered out in
`internal/tui/filepick.go`.

This document records the design work so a later round can pick it up without
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

4. **`run.Attachment` is text-only.** The wire shape in `internal/wire` has no
   multimodal field today. Sending an image to a model therefore needs a new
   attachment kind and provider-specific encoding (OpenAI `image_url`,
   Anthropic `image`, etc.).

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
