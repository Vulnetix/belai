# Settings screens

`/settings` (or `f1`, then `s`) is the browser for the settings file. It shares
its layout, the *grouped list* (`internal/tui/grouplist.go`), with the language
server screen (`/settings` → *language servers*, or `f1` then `l`) and the
permissions editor (`/permissions`). The layout rules are in
[TUI design](tui-design.md#grouped-lists); this page is the behaviour.

Nothing on these screens is a new source of settings. Every row still reads the
resolved settings and every edit still goes through the same write path as
before, so the layering, the project-layer limits and the validation in
[Architecture](architecture.md#settings) apply unchanged.

## Layout

- **Wide (100 columns or more).** A rail of groups on the left, the selected
  group's rows on the right, and a detail pane under the rows.
- **Narrow (under 100 columns).** A one-line tab strip replaces the rail. `‹`
  and `›` mean more groups sit off to that side and `n/m` is the position. The
  strip scrolls so the current group is always on screen.
- **Height.** The body is exactly as tall as the terminal leaves after the
  header, the scope line, any notice, error or open editor, and the key line.
  It never falls under 9 rows, so a very short terminal clips rather than
  overlapping. When a group has more rows than fit, the rows scroll inside
  their box with an `↑ n above · ↓ n below` line; the scroll offset is kept
  between frames, and a heading always stays with the row under it.
- **Detail pane.** It holds up to 4 lines (2 when the body is under 12 rows,
  7 from 30 rows) and takes fewer (never under 2) when the row needs fewer. When the text has
  to be cut, the last line survives, because it says where the value comes
  from and where a change is saved.
- **No width overflow.** Every line is cut to the terminal width, so a long
  path, value or note ends in `…`.

## Groups

Every row belongs to exactly one group. A group with no rows is not on the rail.

| Group | Rows |
| --- | --- |
| General | `provider`, `model`, `effort`, `caveman`, `read-only tools`, `session retention`, `update check`, `auto-commit per task` |
| Display | `banner`, `colours`, `spinner`, `reasoning`, `tool calls`, `file edits`, `internal work`, `layout`, `empty composer pane`, `todo panel`, `mouse capture`, `macOS keyboard hints`, `session names` |
| Tests | `test pass`, `test scope`, `test on fail`, `test command`, `test report`, `test fix passes`, `test timeout` |
| Agents | `max agents`, `plan explore`, `goal explore` |
| Access | `permissions`, `language servers` (each opens its own screen) |
| Budgets | `token budgets` (opens its own screen), `budget cycle`, `budget warnings`, `session intelligence`, `plan limits` |
| Voice | `voice input`, `voice key`, `voice mode`, `voice delivery`, `voice cleanup`, `wake word`, `voice commands`, `voice log` |
| Read aloud | `read aloud`, `read reports aloud`, `read aloud voice`, `read aloud speed`, `read aloud cache` |
| Knowledge | `knowledge per profile`, `knowledge for project`, `knowledge per search`, `knowledge topic chunks`, `knowledge topic documents` |
| Jev jobs | one switch per job: `jev bash swap`, `jev compaction prune`, `jev tool selection`, `jev tool search`, `jev lsp triage`, `jev option order`, `jev explore locate`, `jev voice command`, `jev request scale`, `jev handoff clarity`, `jev gate alignment`, `jev request coverage`, `jev goal judge`, `jev knowledge topics`, `jev agent pick`, `jev teleport verify` |
| Jev thresholds | one slider per cut-off, in the sections below |

### Layout

`ui.layout` (the `layout` row in Display, and a per-project preference) sets how
the thread is laid out. It is display only: both layouts run the same gates and
write the same record.

| Value | What the thread does |
| --- | --- |
| `clean` (default) | Rows as the thread builds them. A role-manager decision that reaches the TUI after rows that happened later is still placed by its own time, and a belai notice is held below a reply while it streams, so the reply is not interrupted. |
| `chronological` | Strict time order. A notice an event raised is placed by the event's time too (never behind the rows already written, so each is written once), nothing is held below a streaming reply, and the blank line above each panel shows that panel's time as `HH:MM:SS`. A row with no recorded time shows none. |

An unknown value reads as `clean`. A project layer may set it either way.

### macOS keyboard hints

`ui.mac_key_hints` (the `macOS keyboard hints` row in Display) adds the Mac form after
an F key wherever Belai names one: `/help`, the Getting started guide, the
rotating tips, the working hint above the composer and the key bars along the
bottom. `f9` reads `f9 (Fn+⏭)`, `f3` reads `f3 (Fn+Mission Control)`, and so on
for `f1` to `f12`.

A Mac keyboard prints a media or system symbol on each F key, and the key sends
that function until Fn is held. Belai's bindings are the function keys, so the
hint says which printed key to press with Fn. If you turn on *Use F1, F2, etc.
keys as standard function keys* in macOS Keyboard settings, the keys work without Fn.

Unset, the setting follows the platform: shown on macOS, hidden on Linux and
Windows. Set it on to see the hints in a virtual machine or a remote session
running on Mac hardware, or off to hide them on a Mac. It is display only.

### Empty composer pane

`ui.idle_pane` (the `empty composer pane` row in Display) picks the one pane
shown above an empty composer while the chat is idle. It is display only: it
reads nothing the pane would not show and changes no permission.

| Value | What shows |
| --- | --- |
| `none` (default) | Nothing: the composer sits on its own. |
| `kanban` | The [kanban pane](kanban.md#the-pane): open items, colour-coded. |
| `activity`, `helpers`, `processes`, `crew`, `git`, `ci`, `intel` | That tab of the runs panel, opened unfocused. A tab that is not offered (`ci` without a pull request, `intel` with `ui.intel` off, `kanban`-board tabs with the board off) shows nothing. |

The pane goes away as soon as you type, attach or start a turn, and comes back
when the composer is empty and the chat idle again. `↓` or `f9` on an empty
composer takes the keyboard in the pane; from then on it is an ordinary runs
panel, so typing leaves it open until you close it with `f9`. A pane you
close stays closed until the composer is next used. An unknown value reads as
`none`. A project layer may set it either way.

**The two Jev groups exist only while a decision backend is configured**
(`Settings.JevConfigured`). Without one they are hidden, not greyed, because no
job runs. See [Jev jobs](jev-jobs.md).

### Jev threshold sections

Every cut-off is in exactly one section, in this order:

| Section | Cut-offs |
| --- | --- |
| security gate | `jev allow at`, `jev deny at` |
| tool and result lists | `jev drop at`, `jev keep at`, `jev strong at` |
| actions | `jev route at`, `jev swap at`, `jev voice at`, `jev simple at` |
| language server triage | `jev triage at` |
| goal judge | `jev goal complete at`, `jev goal rival max`, `jev goal not started at` |
| locate | `jev hit at`, `jev lead at` |
| option order | `jev option hit`, `jev option margin`, `jev option lead` |
| mode choice | `jev mode sure`, `jev mode margin`, `jev mode headless` |
| delivery crew | `jev clear at`, `jev align at`, `jev cover at` |
| knowledge topics | `jev topic at` |

A section heading labels its rows and never takes the cursor.

## Keys

| Key | Action |
| --- | --- |
| `↑` `↓` (`k` `j`) | Move inside the group. It stops at the first and last row; it does not run into the next group. |
| `[` `]`, `tab` `shift+tab` | Previous or next group, wrapping. The cursor lands on the group's first row. |
| `←` `→` | Off a slider, the same as `[` `]`. On a slider they move the value by 0.05 (`shift+←` `shift+→`, `H` and `L` move it by 0.01). |
| `space`, `enter` | Toggle, cycle a choice, open a text editor, or open a submenu. |
| `x` | Unset the value back to its default. |
| `s` | Switch the scope the next edit is written to, between project and global. |
| `/` | Filter. Type to narrow, `enter` keeps the filter, `esc` clears it. |
| `m` | Show only the rows whose value differs from the default. |
| `esc` | Close a filter or the changed-only view first; on the next press, leave the screen. |

The key line drops its least important keys first when the width runs out, so
movement, edit and back always stay.

## Business rules

- **Where a change is saved.** The detail pane's last line says so. Rows the
  project layer may not set are always written to your global settings whatever
  the scope chip shows: `auto-commit per task`, `plan limits`, every `test …`,
  `voice …`, `read aloud …` and `knowledge …` row, and every Jev threshold. Other rows follow
  the scope (`s`), which starts as project and is remembered for the session
  once chosen.
- **A higher layer wins.** After an edit, if another layer sets the same key and
  outranks the one written, a notice says which layer wins. The value shown is
  always the effective one.
- **The source column** is blank for a default and shows the layer name
  (`global`, `project`, `project-prefs`, `state`, `env`, `flag`) for anything
  else. The rail's `●` marks a group with at least one such value.
- **Dependent rows dim, and stay editable.** `test …` rows (except `test pass`)
  read as muted while `test pass` is `off`; `voice …` rows (except `voice
  input`) while voice input is off; `read aloud …` rows (except `read aloud`)
  while read aloud is off. The detail pane says what they wait on. Dimming never
  blocks an edit, so a value can be prepared before its switch is turned on.
- **Score cut-offs are checked before they are written.** A move that leaves 0
  to 1, or breaks one of the rules below, is refused with the rule's message and
  nothing is saved. Stepping past either end of the track does nothing.
  - `allow at` is at most 0.50 and `deny at` at least 0.50, and `deny at` exceeds
    `allow at` by at least 0.05 (the detail pane shows the live gap).
  - `drop at` does not exceed `keep at`, which does not exceed `strong at`.
  - `voice at`, `simple at` and `goal complete at` are at least 0.50, as is
    `goal not started at`; `goal rival max` is at most 0.50.
  - `lead at` does not exceed `hit at`.
  The project layer cannot set a cut-off, and a saved change applies at once.
- **A track** is 20 cells with a `│` at the default, so a value reads against
  where it starts.
- **Editing a text row** (`provider`, `model`, `session retention`, `max agents`,
  `budget cycle`, `test command`, `test fix passes`, `test timeout`, `knowledge per profile`, `knowledge for project`, `knowledge per search`, `knowledge topic chunks`, `knowledge topic documents`) opens the
  editor below the list, and the body shrinks by its height. `enter` saves,
  `esc` cancels; a refused value stays open with the reason.
- **Settings with no row.** Some keys are edited in `settings.json` only: the
  library sync switches (`sync.skills`, `sync.prompts`, `sync.commands`, `sync.processes`, `sync.repos`, `sync.budgets`, `sync.rewrites`, `sync.providers`, `sync.mcps`, `sync.hooks`; see
  [session-sync.md](session-sync.md#settings) and [library-items.md](library-items.md))
  and the `repos` list have no row here. A project layer may switch a sync switch off
  and never on, and its `repos` is dropped.

## Edge cases

- **Filter.** It matches a row's label, key and help across every group, ignoring
  case. The rail and tab strip give way to one *Results* list, each row naming
  its group in the detail pane, and `[` `]` do nothing. If the filter hides the
  selected row, the cursor moves to the first match instead of vanishing. With
  no match the list says `no setting matches`.
- **Changed only** combines with a filter: both must hold.
- **A cursor left on a hidden row** (a Jev backend removed while the screen is
  open) snaps to the first visible row.
- **Zero size.** When the terminal height is not yet known (0), nothing is
  clipped, as on the `/model` screen.
- **Sliders and group keys.** Because `←` `→` nudge on a slider row, use `[` `]`
  or `tab` to leave the Jev thresholds group from one.

## Language servers

Rows are split into **Detected** and **Not detected** by the last probe (see
[LSP](lsp.md#tui)). A turned-off language shows `off` in the source column and
is dimmed. The detail pane shows the server, the note and, for a language that
is not detected and has an install command, the exact command. Keys: `↑` `↓`,
`[` `]` (and `←` `→`, `tab`), `space` toggle, `x` unset, `i` install (asks
first, `y` to run), `r` re-detect, `esc`. The edit is saved to the scope chosen
in `/settings`. Windows shows a single line saying servers are unsupported.

## Permissions

Rules are grouped **Deny**, **Allow**, **Ask** in that order (the order the list is
built), each group tinted with its decision colour.
A decision with no rules has no group; with no rules at all the list says every
tool call is allowed. A row's marks are `? unknown tool` (no registered tool has
that name), `! broad` (no argument limit) and `inherited` (from another layer,
shown muted, and never edited or deleted here). Keys: `↑` `↓`, `[` `]` and `tab`
for groups, `a` add, `e` edit, `d` delete, `←` `→` cycle the decision (deny, ask,
allow), `p` preview a call, `s` scope, `esc`. The preview, the editor and an
error sit under the list and take rows from it. Rule matching is unchanged: a
deny fires on any command a line contains and an allow must cover all of them.
