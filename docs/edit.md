# Tolerant Edit matching

**Status:** new.

`Edit` replaces `old_string` with `new_string` in a file the model has read. An
exact match always wins. When there is none, `Edit` retries on whole lines
through a fixed ladder of whitespace-only relaxations and applies the first
step that finds exactly one place. When nothing matches, the error names the
closest lines and how they differ. The harness decides all of it by comparing
bytes. No model is asked whether a match is safe.

## The ladder

`fuzzy.Locate` (`internal/fuzzy/lines.go`) walks these tiers in order. A tier
that finds nothing falls through to the next. The first tier that finds
anything decides, and the number of places it found is what counts.

| Tier | Name in the result | Lines count as equal when |
| --- | --- | --- |
| exact | (not named) | the bytes are equal, as a substring |
| line endings | `line endings folded` | they are equal once CRLF, LF and a lone CR are one terminator |
| trailing | `trailing whitespace ignored` | they are also equal once trailing spaces and tabs are removed |
| indent | `indentation shifted` | they are also equal once one and the same leading whitespace prefix is added to, or removed from, every non-blank line |

The relaxed tiers compare whole lines, never part of one, and need at least one
line of `old_string` that is not blank. In the indent tier a blank line matches
any blank line. One place applies. More than one place fails with the count. Exact
matches keep the old rule: more than one without `replace_all` fails.

`replace_all=true` is always exact. A relaxed match is applied to one place
only, so it can never rewrite several lines the model did not spell out.

## Fitting the replacement

`fuzzy.Refit` and `fuzzy.Apply` fit `new_string` to the file: its line
terminators become the file's, and under the indent tier each non-blank line
gains or loses the same prefix the match had. These refuse, and nothing is
written, when:

- the matched lines mix line endings (for a match with no line break inside it,
  the file's line endings are the test);
- the indentation delta mixes tabs and spaces;
- `new_string` has a line that does not carry the indentation to be removed.

The size, binary and read-before-edit checks, the permission ask, the sandbox,
hooks, path confinement and the atomic write are unchanged and run first. A
failed edit leaves the file byte-identical. The approval diff (`Edit.Preview`)
and the written file come from the same plan, so they are equal for every tier.
The result names the tier when it was not exact, for example
`edited f.go (1 replacement, matched with indentation shifted)`.

## Near-miss diagnostics

When there is no match at any tier, `fuzzy.Nearest` ranks up to three places in
the file that resemble `old_string`. The first non-blank line of `old_string` is
scored against each line of the file with `fuzzy.Score` in both directions, a
line that is equal after trimming ranks first, and a line that shares at least
half its character pairs ranks below those. Candidates inside one another's
window collapse to the better one. At most 20000 lines are scored and at most
400 runes of a line are compared.

Each candidate reads `line N (KIND differs): snippet`. `KIND` is measured by the
harness, never read from the snippet: `line-ending`, `trailing-whitespace`,
`indent` or `content`. The snippet is one line, cleaned with `sanitize.Line`
and capped at 200 runes.

If the classifier withheld an earlier `Read` of the file in this session
(`agent.flaggedFiles`), the snippet is left out and the message says so. The
line numbers and the kind of difference remain. The error is still plain
`KindEdit` output: sanitised, with no classifier round trip, because the
harness composed it and the one piece of file text it can carry is gated this
way.

## Setting

| Key | Meaning |
| --- | --- |
| `edit.tolerant` | `true` (the default) enables the ladder and the diagnostics. `false` makes `Edit` match byte for byte and return its plain errors. |

Your own settings layers set it either way. A project settings file may only
turn it off (`mergeEdit` in `internal/config/edit.go`, called from both
`Settings.Override` and `Resolve`); its `true` is ignored. There is no flag,
environment variable, slash command or tool argument for it, and a model cannot
change it. A hand-built `tools.Edit` is strict until its `Tolerant` field is set;
every session sets it from the resolved settings.

## Limits

- Tabs against spaces is not converted. An old string indented with spaces
  does not match a file indented with tabs, because neither indentation is a
  prefix of the other. The diagnostic reports it as an `indent` difference.
- A relaxed match never edits a partial line.
- No content is guessed: a typo in a word is a miss, and the diagnostic is the
  help.

## Where it lives

- `internal/fuzzy/lines.go`: `Locate`, `Refit`, `Apply`, `Nearest`, `Tier.String`
  and `Problem.String`.
- `internal/tools/edit.go`: the plan shared by `Execute` and `Preview`, and
  `WithEditWithheld`, the context value the agent sets on each call.
- `internal/config/edit.go`: `EditSettings` and `Settings.EditTolerant`.
- `internal/agent/agent.go`: `NewSession` applies the setting to the registry's
  `Edit`, and `executeCall` supplies the withheld-file check.
