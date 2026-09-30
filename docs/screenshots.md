# Screenshots

`Screenshot` lets the model see what it built: start a dev server with
`Bash run_in_background` (see [supervised processes](architecture.md#model-started-background-processes)),
capture the page, read the result, change the code, capture again. It can also
capture the desktop, after asking you.

Images you attach yourself (`@shot.png`, the clipboard, a dropped file or an ACP
image block) take the same path; see [Images](image-attachments.md).

The image path (why no classifier reads it, how it is checked and how it
reaches each provider) is in [Images](image-attachments.md). This page is the
tool.

## Arguments

| Argument | Meaning |
| -------- | ------- |
| `target` | `url` (a loopback page) or `desktop` (the whole screen; offered only while `screenshot.desktop` is on) |
| `url` | For `url`: the page. Only `http` or `https` on this machine |
| `width`, `height` | For `url`: the viewport, default 1280 by 800, clamped to 320 to 3840 and 240 to 2160 |
| `wait_ms` | Time for the page to settle before the capture, default 500, at most 10000 |

Nothing else is a model argument. The programs that run and their arguments
are fixed by the harness.

## Loopback pages

- The URL goes through `internal/netguard` (the one URL policy: no credentials,
  no numeric or short IP forms, no backslashes or control characters, no
  escapes that change on decoding) and must then name this machine: `localhost`
  or a loopback address. There is no second loopback test.
- Belai checks that something is listening on the host and port before it
  starts a browser, and says so if not (start the server first).
- The browser is the first of `google-chrome`, `google-chrome-stable`,
  `chromium`, `chromium-browser` or `chrome` on `PATH` (or Google Chrome in
  `/Applications` on macOS), run headless with a throwaway profile directory,
  the scrubbed environment and its own process group. Its sandbox is never
  turned off, and it is not wrapped in the OS sandbox, which would break the
  browser's own.
- Requests the page makes to any other host fail: every name except
  `localhost` is refused by the resolver rules, and a dead proxy is set so an
  address that is not a name (which the resolver rules do not see) cannot be
  reached either. A page on the loopback cannot pull the browser to another
  site.
- No page text or DOM is returned. The model gets the image and a line the
  harness composed.

## The desktop

- Every desktop capture asks, whatever the permission rules, the ask gate or
  `allow always` say (`Screenshot.AlwaysAsksFor`). Without anyone to ask, the
  call is withheld.
- The programs, tried in order and run with a fixed argument list: on Wayland
  `grim`; on X11 `maim`, `scrot`, ImageMagick `import`, `gnome-screenshot`; on
  macOS `screencapture`. If none is on `PATH` the error names what to install.
  A machine with neither `WAYLAND_DISPLAY` nor `DISPLAY` set has no display,
  and the error says so. Windows is not supported.
- Capturing a single window is not supported.

## Where it is available

The interactive TUI session only. Headless runs, ACP, fleet workers and remote
control sessions do not register it, because a capture asks and they cannot.
An engaged background definition that names its tools gets `Screenshot` only
if it lists it.

## Settings

| Key | Effect |
| --- | ------ |
| `screenshot.enabled` | Default on. Off removes the tool |
| `screenshot.desktop` | Default on. Off leaves loopback pages only |

A project settings file can turn either off and never on, so a repository
cannot enable capture for a user who did not.

## Permission rules

The rule subject is `desktop`, or `url:` followed by the canonical URL, so
`Screenshot(url:http://127.0.0.1:*)` allows your dev server without a prompt
each time and never allows the desktop. A deny rule such as
`Screenshot(desktop)` blocks it outright.

## Files

Each capture is written as a PNG under `~/.vulnetix/belai/screenshots`
(`shot-*.png`, the newest 20 are kept). The path is in the result, so you can
open it. The model is not given a way to read it back: the image it saw is the
one in the conversation, and it is never re-read from disk.

## Edge cases

- **The model has no image input.** The capture is still written and its path
  reported, and the result tells the model the image was not sent.
- **A capture over 32 MiB, an empty file, a file that does not decode as PNG
  or JPEG or one over the pixel budget** is an error or a refusal note; no
  partial image is sent.
- **A timeout** (40 seconds for the whole call) kills the capture program's
  process group and reports which program timed out.
- **A failed browser** reports the last few lines it printed, cleaned and
  capped, and leaves no file behind.
- **Text in the image** is data. The result says so, and an image never widens
  what the permission rules allow the model to do next.
