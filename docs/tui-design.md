# TUI design system

**Status:** alpha-20260927, in part. The palette, open thread blocks, quiet
tool rows, collapsed reasoning, the one-line header and the dotted footer
switches have shipped; [Not yet](#not-yet) lists the rest. The palette lives
in `internal/tui/components/theme.go`.

Belai's terminal UI is quiet by default and loud on purpose. Structure is
drawn in the dimmest colour on screen, and colour always carries a meaning —
who is speaking, what failed, what needs you — never decoration. The composer
is the only box, because it is the one place you type.

The mockups (before, after in dark and light, states, and this system) are on
the design canvas: <https://claude.ai/artifact/WVcV4QJc1nwj4YQMyierwu>.

- [Principles](#principles)
- [Colour roles](#colour-roles)
- [Glyphs](#glyphs)
- [Rhythm](#rhythm)
- [Surfaces](#surfaces)
- [States](#states)
- [Not yet](#not-yet)
- [Changing the system](#changing-the-system)

## Principles

1. **Dim structure.** Rules, fills and connectors use `Line`. A reader should
   see the content first and the frame only when looking for it.
2. **Colour is a signal.** Each accent has one job (see below). A colour used
   for decoration stops meaning anything when it is needed.
3. **Top edges only.** A thread block keeps its top rule, because the title
   and metadata on it say where a block starts and what it is. Side and
   bottom edges are noise; indentation carries the nesting.
4. **One box.** The composer keeps its full frame so it reads as the input.
   Full-screen views and overlays (pickers, trust gate, agent and process
   views) are not the thread and keep their frames.
5. **Fills are for danger.** A solid-background chip appears only when a
   safety switch is relaxed (guardrails off, YOLO). Everything else is text.
6. **Only failures shout.** Routine tool calls are low-contrast rows; a
   failing row turns red and its output opens by itself.

## Colour roles

Each role is a `lipgloss.AdaptiveColor` with a dark and a light value. Light
values are darker variants of the brand hues: the dark-terminal accents are
unreadable on a light background.

| Role | Dark | Light | Used for |
| --- | --- | --- | --- |
| `ColorLine` | `#2F4340` | `#C6D5D2` | Rule fills, trailing `──`, the composer frame while it is inactive. Never text you need to read. |
| `ColorLow` | `#4A5F5B` | `#7F908C` | Metadata you may ignore: token counts, timings, the `⌁` glyph, hints, session id. |
| `ColorMuted` | `#7D918D` | `#5C6E6B` | Paths, tool arguments, tool output, reasoning, footer labels. |
| `ColorText` | `#C9D6D2` | `#2A3835` | Body copy of model replies. Softer than `ColorCream` so emphasis has somewhere to go. |
| `ColorCream` | `#F6EED6` | `#0F1F1C` | Emphasis: what you typed, identifiers the model names, the model id. |
| `ColorTeal` | `#3AC4B4` | `#137A6F` | The model's voice, the active composer, success `✓`, bullets, switches that are on. The one brand accent. |
| `ColorTealSoft` | `#76E0CD` | `#1A8C7C` | Your turns, plan mode, keycaps. |
| `ColorAmber` | `#E8912B` | `#A95A0B` | Needs you, or you stepped in: permission asks, steering, `!` shell, goal mode, YOLO, context over 80%. **Not** routine tool activity. |
| `ColorDanger` | `#E2564E` | `#B8322A` | Failed rows, errors, guardrails off. |
| `ColorDiffAddBg` | `#11301F` | `#DFF1E6` | Background of added diff lines. |
| `ColorDiffDelBg` | `#3A1917` | `#F9E2E0` | Background of removed diff lines. |

`ColorInk` (`#1C3431`) is the foreground on a solid chip.

Contrast: every role that renders text you must read (`Muted` and up) keeps
4.5:1 against the terminal background in its theme. `Line` and `Low` are
deliberately below that and never carry required information on their own.

## Glyphs

| Glyph | Colour | Meaning |
| --- | --- | --- |
| `──` | role accent | Lead-in of a thread block's top rule; the fill after the title is `Line`. |
| `⌁` | `Low` (`Danger` on a failed row) | A tool row. |
| `✓` | `Teal` | Succeeded. |
| `✗` | `Danger` | Failed; the row's output auto-expands. |
| `⠹` (spinner) | `Teal` | Running. `•` when `ui.spinner` is off. |
| `∴` | `Low` | Reasoning, collapsed to one line (`∴ thought for 6s · ctrl+o`). |
| `◆` `◇` | `TealSoft` / `Low` | Plan step: current, pending. Done steps use `✓`. |
| `●` `○` | `Teal` / `Low` | A footer switch: on, off. |
| `›` | mode accent | The composer caret. |
| `⎇` | `Teal` | Git branch. |
| `▰` | `Teal` / `Line` | Context gauge: used, free (amber over 80%, danger over 95%). |
| `…` | `Low` | A truncation hint (`… 16 more lines`). |

Never use emoji. A new glyph must be in this table before it ships.

## Rhythm

- **Block spacing.** One blank line between thread blocks, and no bottom
  edge. The next block's top rule is the separator.
- **Indent.** Body text starts two columns in; tool output and diff rows six.
  The eye reads the nesting without a vertical bar, and the body column
  matches the old `│ ` gutter so copy and hit-testing keep their offsets.
- **Top rule.** `── title  detail ────────── meta ──`: two lead-in dashes in
  the role accent, the title bold in the accent, an optional `Low` detail,
  a `Line` fill, right-aligned `Low` metadata, and a `Line` trailing `──`.
  When the width runs out the metadata is dropped before the title is
  truncated.
- **Right column.** Tool-row status and metadata are right-aligned and end
  two columns before the edge.
- **Header.** One line: `belai`, version, working directory, branch, and on
  the right the session id and age. The owl banner appears only on
  `/welcome` and a first run.
- **Hint line.** One `Low` line directly above the composer, only while a
  turn is in flight.
- **Footer.** Three lines under the composer, with no rule: the composer's
  bottom edge is the separator. Line 1 is the mode (coloured text, not a
  chip), working directory and branch, with the budget gauge on the right.
  Line 2 is provider, model and effort, the switches as `●`/`○`, and the
  session, token count and context bar on the right. Line 3 shows the armed or
  hover hint while there is one and the subagent roster otherwise; it is
  always reserved so the footer never changes height under the pointer.

## Surfaces

| Surface | Frame | Title accent |
| --- | --- | --- |
| Your prompt | top rule | `TealSoft`, titled `you` |
| Steering | top rule | `Amber`, titled `you · steering` |
| Model reply | top rule | `Teal`, titled `model` plus the model id |
| Reasoning | none; one `∴` line, expands to a `Muted` block | `Low` |
| Tool group (`belai`) | top rule | `Muted` title, `Low` lead-in |
| `!` shell | top rule | `Amber`, titled `! shell` with the exit code |
| Permission ask | top rule | `Amber`, titled `permission` with the tool |
| Error | top rule | `Danger`, titled `error` with the source |
| Plan / todos | top rule | `TealSoft`, titled `plan` with progress |
| Composer | full rounded box | mode colour; `Line` while a question is open |
| Full-screen views, pickers, overlays | full rounded box | unchanged |

## States

- **Turn in flight.** The running row shows its elapsed time and a spinner;
  the hint line reads `⠹ working  14s · esc interrupt`.
- **Permission ask.** The amber block carries the command in `Cream`, a
  `Low` line with the rule and sandbox facts, and the keys as `TealSoft`
  keycaps. The composer frame drops to `Line`.
- **Failure.** The failed row's `⌁`, name and `✗` turn `Danger`, and its
  output expands. The group's rule adds `· 1 failed`.
- **Relaxed safety.** `guardrails off` is a `Danger` chip; both guardrails
  and ask off is an `Amber` `YOLO` chip. These are the only solid chips.

## Not yet

- Reply body copy still renders in the terminal's default foreground rather
  than `ColorText`.
- The composer has no `›` caret, the context gauge still uses block eighths
  rather than `▰`, and the gauge does not yet turn amber above 80%.
- A permission ask is still its own view rather than an amber block in the
  thread, and the composer does not drop to `Line` while it is open.
- A failed tool row does not yet expand its output by default, and there is no
  `error` block for provider errors.
- The subagent roster and review still use chips.

## Changing the system

A change to a role, glyph or rhythm rule updates this page in the same
commit, and the row renderers' golden file
(`internal/tui/components/testdata/rows.golden`, refreshed with
`UPDATE_GOLDEN=1 go test ./internal/tui/components/`).
