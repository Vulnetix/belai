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
| `ColorTealSoft` | `#76E0CD` | `#1A8C7C` | Plan mode, keycaps, the dimmer beat of the voice icon pulse. |
| `ColorYou` | `#F49AC8` | `#B23C7E` | The `you` title of a prompt you typed. |
| `ColorVoice` | `#C9B0F2` | `#7B5BBE` | The `you` title of a dictated prompt, and the composer frame while the speech or fast model works. |
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
| `▁▂▃▄▅▆▇█` | state colour (`Low` for an idle cell) | Sparkline of session intelligence: one cell per group of hours, scaled to the busiest cell. |
| `╹` | `Low` | Reset marker on a limit bar: how much of the window has passed. Fill beyond it is ahead of the clock. |
| `░▒▓█` | `Line` / `Low` / `Teal` / `TealSoft` | Heatmap cell of the 7-day by 24-hour view, four steps quantised against the busiest hour. |
| `↗` `→` `↘` | `Amber` / `Teal` / `Teal` | Trend against the 7-day average: rising, steady, easing. |
| `······` | `Low` | A picture with nothing to draw yet (no usage recorded). |
| `…` | `Low` | A truncation hint (`… 16 more lines`). |
| `◌` | `Amber` | A drafted offer the user has not taken yet (agent builder). Taken turns `✓` `Teal`; a default value is a `Low` `·`. |
| `┄` `┆` | `Amber` | A drafted route between agents, horizontal and vertical (agent builder's relationship lane). |
| `▶` `◀` | `Line` | The direction of a settled route between agents. |
| Braille dots `⠀`-`⣿` | `Teal` / `Voice` / `Low` | The composer's voice microphone: a capsule on a holder, drawn as an 18 by 12 dot bitmap in three rows of nine cells (solid, soft dither or outline), sound arcs while speech is heard, a slash when muted. |
| `⎽⎼⎻⎺` | `Teal` | The composer frame's slow ripple while speech is heard. |
| `◉` `◎` `⊘` `◌` | `Teal` / `TealSoft` / `Low` | The small voice icon at the right end of the composer's first row, drawn only where typed text would sit under the microphone. |

`◌`, `▶`, `◀`, `┄` and `┆` are East Asian ambiguous-width. Where go-runewidth
reports them as two cells they fall back to `o`, `>`, `<`, `-` and `:` so that
columns stay aligned.

Never use emoji. A new glyph must be in this table before it ships.

### Role-manager rows

A role-manager decision is one line: `icon summary — outcome marker · decider time`.
The icon names the activity and takes its category colour. The outcome word
keeps its tone colour (teal clear, amber caution, danger blocked, muted
neutral) and a marker, so the result reads without hue. The decider is the
short model id in the Jev accent (`jev-1.13`), a chat model's id in `Muted`, or
`belai` in `Low` for deterministic code; "belai" is never the name of a Jev or chat
decision. The summary is what gives way when the line is short, then the
decider tag; the icon and the outcome stay. Ctrl+O adds a dim second line with
the activity key, the full provider/model, the category, the score and the
cause.

| Marker | Colour | Meaning |
| --- | --- | --- |
| `✓` | `Teal` | The decision cleared it. |
| `!` | `Amber` | Caution. |
| `✗` | `Danger` | Blocked. |
| `↩` | outcome | It fell back to another decider. |
| `⟳` | outcome | It came from a cache. |
| `◔` | outcome | It timed out. |

A category colours the icon and nothing else. Tone colours the outcome, so
what the work was and how it went never share a channel.

| Category | Token | Dark | Light |
| --- | --- | --- | --- |
| `security` | `ColorCatSecurity` | `#B58AE6` | `#7A3FB0` |
| `mode` | `ColorCatMode` | `#6FA8E8` | `#2C6FB7` |
| `context` | `ColorCatContext` | `#D2B24A` | `#8A6D0B` |
| `tools` | `ColorCatTools` | `#4FC3E0` | `#0E7C9B` |
| `code` | `ColorCatCode` | `#8CCB5E` | `#3F7D20` |
| `ask` | `ColorCatAsk` | `#E58AB8` | `#B0357A` |
| `routing` | `ColorCatRouting` | `#A99BD6` | `#6B5B95` |
| `housekeeping` | `ColorLow` | `#4A5F5B` | `#7F908C` |

One icon per activity. A glyph a terminal does not draw one cell wide falls
back to its category's letter (S, M, C, T, L, ?, R, .) and the markers to
`<`, `=` and `~`.

| Icon | Activity | Category |
| --- | --- | --- |
| `◈` | security verdict | `security` |
| `◇` | verdict unclear | `security` |
| `▸` | security phase | `security` |
| `◊` | security fallback | `security` |
| `↺` | verdict recalled | `security` |
| `↯` | recalled verdict rejected | `security` |
| `⊕` | boundary sealed | `security` |
| `⊘` | boundary check failed | `security` |
| `≠` | tool call mismatch | `security` |
| `◐` | mode chosen | `mode` |
| `◎` | intent detected | `mode` |
| `⊙` | mode pinned | `mode` |
| `⊤` | goal length limit | `mode` |
| `◉` | goal check | `mode` |
| `↻` | goal check repaired | `mode` |
| `≡` | plan check | `mode` |
| `◍` | agent check | `mode` |
| `✎` | goal drafted | `mode` |
| `⊟` | compaction summary | `context` |
| `⊠` | compaction prune | `context` |
| `▤` | page answer | `context` |
| `⊡` | dependency change | `context` |
| `⊨` | test report | `context` |
| `◖` | dictation tidied | `context` |
| `◗` | spoken instruction matched | `ask` |
| `⇄` | Bash swapped for a builtin | `tools` |
| `⇆` | Bash replanned | `tools` |
| `⊛` | tool and skill search | `tools` |
| `⊞` | tool and skill selection | `tools` |
| `⌘` | language server found | `code` |
| `⌥` | diagnostics | `code` |
| `⊗` | language server down | `code` |
| `▣` | LSP triage | `code` |
| `⌖` | files located for a question | `code` |
| `?` | question asked | `ask` |
| `≣` | options ordered | `ask` |
| `⇢` | route fallback | `routing` |
| `◫` | agent pool admission | `routing` |
| `✦` | session named | `housekeeping` |
| `·` | anything without its own icon | `housekeeping` |

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
  chip), working directory and branch, with the budget gauge on the right. The
  right side cycles through the model's budgets and ends on the session
  intelligence slot (`intel`, today's tokens, the tightest plan limit or the
  pace, and a limit bar or sparkline); that slot is the only one when routing
  is `routed` or the model has no budget.
  Line 2 is provider, model and effort, the switches as `●`/`○` (with
  `● rc N` while `belai rc` runs), and the session, token count and context
  bar on the right. Line 3 shows the armed or
  hover hint while there is one and the subagent roster otherwise; it is
  always reserved so the footer never changes height under the pointer.

## Surfaces

| Surface | Frame | Title accent |
| --- | --- | --- |
| Your prompt | top rule | `You` (pink), titled `you` |
| Your dictated prompt | top rule | `Voice` (pastel purple), titled `you`; `ctrl+o` shows the raw transcript |
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
| Runs panel, intel tab (`f12`) | tab bar and a bottom rule, half the terminal height | `Teal` for the active tab; bars and marks take the state colour |

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
- **Voice on the composer.** While voice input runs, a microphone is drawn
  in the composer: a capsule on a U-shaped holder with a pole and a base,
  an 18 by 12 dot bitmap shown as three rows of nine Braille cells, starting
  on the first text row and flush right. It is a `Teal` solid mic
  that pulses to a dithered one while the microphone is open, gains one
  and then two sound arcs on each side while speech is heard, a `Voice`
  (pastel purple) mic alternating solid and soft while the speech or fast
  model works, a still outlined `Low` mic when voice is waiting for its
  key or paused, and that mic with a slash through it when muted. Where typed
  text reaches the mic's columns it gives way to a one-cell icon at the right
  end of the first row. While speech is heard the frame's top and bottom
  rules ripple slowly (scan-line glyphs `⎽⎼⎻⎺` at staggered heights); while
  the speech model or the fast model works the frame turns `Voice`, and
  returns to the mode colour when the text lands. `docs/voice.md` has the
  states and the timings.
- **The read-aloud player card.** `ctrl+b` or `tts.read_reports` puts an open
  `Voice` panel in the thread titled `read aloud`, with the first words of the
  reply and `state · speed` on the rule. Three rows: a transport row (`◂◂ 10s`,
  `❚❚ pause` or `▶ play` or `↻ replay` or a braille spinner, `■ stop`, `10s ▸▸`,
  then the speed chips `0.75× 1× 1.25× 1.5× 2×` with the active one in `Cream`
  and brackets); the scrub bar `1:12 ━━━●─── 3:40+` (`━` and the `●` knob in
  `Voice`, `─` in `Low`, a `+` while audio is still arriving); and 24 level bars
  `▁▂▃▄▅▆▇█` in `Teal` while playing and `Low` otherwise, then the status. The
  buttons and the bar are clickable and the bar drags; on a terminal under 62
  cells the button words drop. The panel turns `Danger` on failure. It is
  render-only and ephemeral. `docs/tts.md` has the behaviour.

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
- The premise-first agent builder is designed but not built. The design is in
  the `Tui120`, `Tui80`, `TuiRel` and `TuiKeys` screens on the design canvas
  <https://claude.ai/artifact/F8FCswyhCAa2r6X8AXLnFg>, and `◌`, `┄` `┆` and
  `▶` `◀` above are reserved for it. The drafter it will use,
  `internal/agentdraft`, already serves the website's builder and
  `belai agent draft`.

## Changing the system

A change to a role, glyph or rhythm rule updates this page in the same
commit, and the row renderers' golden file
(`internal/tui/components/testdata/rows.golden`, refreshed with
`UPDATE_GOLDEN=1 go test ./internal/tui/components/`).
