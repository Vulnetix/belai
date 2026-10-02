# Sanitisation

Belai cleans text with deterministic Go code, one function per destination. No
model is asked whether a value is safe. What a string may safely contain
depends on where it is going: text for a model, a line in the terminal, a file
path, a URL, a shell command and a request to a decision backend are different
problems with different rules, so each has its own constructor and none is used
for another.

This page is the reference for those rules and their edge cases. The
[security invariants](../AGENTS.md) say what
must never be weakened; [architecture](architecture.md#delimiter-nonce-and-integrity-model) says where
the sanitiser sits in the tool-result pipeline.

## The sinks

| Sink | Where | Function | Refuses or repairs |
| --- | --- | --- | --- |
| Delimiter markup in any text | `internal/sanitize` | `Sanitize` | Repairs (removes the tag) |
| Free text for a model or transcript | `internal/sanitize` | `Text`, `TextN` | Repairs |
| Dictated speech, before and after the `voice_cleanup` role | `internal/sanitize` | `Text` | Repairs |
| One line of UI, log or notification text | `internal/sanitize` | `Line`, `Clip` | Repairs |
| Identifier or label | `internal/sanitize` | `Ident` | Repairs |
| State for a decision backend (Jev) | `internal/sanitize` | `ForDecision` (returns `DecisionText`) | Repairs |
| File path, before resolution | `internal/sanitize` | `PathText` | Refuses |
| HTTP header value | `internal/sanitize` | `Header` | Refuses |
| URL a model asked to fetch | `internal/netguard` | `CheckURL` with the `Fetch` profile | Refuses |
| URL a user configured for a service | `internal/netguard` | `CheckURL` with the `Endpoint` profile | Refuses |
| Address a connection would reach | `internal/netguard` | `Forbidden`, `ForbiddenIP` | Refuses |
| Loopback test used by all of the above | `internal/netguard` | `IsLoopbackHost` | n/a |
| Shell command line | `internal/shellsafe` | `Analyze`, `ReadOnly`, `Clean` | Refuses |
| Tool argument | `internal/tools` | `CheckArgs`, `CheckFormat` | Refuses |
| Profile fact, and the flag a cloud tool may carry | `internal/factspec` | `Validate`, `Bind` | Refuses |

Repairing is used for text whose content has value even when part of it is
hostile (a tool result, a prompt): the dangerous part is cut and the rest
survives. Refusing is used where a repaired value would be a different value: a
path, a URL, a header or a command. A refused call returns a message naming the
argument so the model can correct it.

## Text bound for a model: `Text`

`Text` is `Sanitize` plus the character-level rules:

- Invalid UTF-8 bytes are dropped.
- Terminal escape sequences are removed whole (`ESC [ ... `, `ESC ] ... BEL`),
  not just the escape byte, so no `[2J` is left behind.
- Control characters go, except newline and tab. Carriage returns are dropped,
  so CRLF becomes LF. NEL, U+2028 and U+2029 become newlines.
- Bidirectional controls, zero-width and other format characters, variation
  selectors and the tag block go. ZWJ and ZWNJ are kept because they spell real
  text.
- Delimiter markup is removed last, on the cleaned text.

`Text` is idempotent: cleaning cleaned text changes nothing. `Line` folds all
whitespace to single spaces; `Clip` also caps the length and appends an
ellipsis after at most `max` runes. The ad hoc copies of this logic in the
firewall, forge, TUI and session-sync code now call these functions.

### Delimiter markup: `Sanitize`

Harness blocks (`system`, `agent`, `plan`, `goal`, `tools`, `skills`, `hooks`,
`attachment`, `exploration`, `directive`, `diagnostics`) are removed, opening
and closing. Any other tag stays, minus `nonce` and `integrity` attributes.
The scan is made on a folded form of the text, so these do not hide a tag:

| Trick | Example |
| --- | --- |
| Case | `<SYSTEM>`, `</SyStEm>` |
| Space inside the brackets | `< system >`, `</ system>` |
| Line break inside the tag | `<system` newline `nonce="x">` |
| Full-width or small-form brackets | U+FF1C, U+FF1E, U+FE64, U+FE65 |
| Full-width letters | full-width `system` |
| Zero-width, soft-hyphen, tag-block or other format characters inside the name | `<sys` U+200B `tem>` |
| Character references | `&lt;system&gt;`, `&#60;system&#62;`, `&#x3c;` |
| Fragments that join after a removal | `<sys<system>tem>` |

A tag is not allowed to contain another `<`, so an inner tag is found first;
the scan repeats until nothing changes (at most 32 passes). Input nested deeper
than that fails closed: the remaining angle-bracket characters are dropped.
A quoted `>` inside an attribute ends the tag early, which leaves harmless
text behind, never a tag. Ordinary comparisons (`a < b`) are untouched, and
text with no `<`, `&` or full-width bracket takes a fast path that returns it
unchanged.

The set of non-ASCII characters that fold to `<` is fixed by Unicode; a test
recomputes it so the fast-path constant cannot drift.

## Decision backends: `ForDecision`

A decision backend (Jev) answers with numbers, but its prompt is still text, and
content that imitates the prompt's structure can change how a small model reads
it. `ForDecision` returns a `DecisionText`, a type only this function can build.
Functions that assemble a decision request take that type, so the compiler
proves the sanitiser ran (`DecisionText.String` returns the text and
`DecisionText.Len` its byte length). It applies `Text`, then:

- splits chat special-token openers in any width (`<|`, `<s>`, `</s>`,
  full-width forms) by inserting a space after the bracket;
- rewrites `[INST]` and `[SYS]` markers;
- prefixes with `| ` any line that begins `Question`, `Options:`, `Answer`,
  `Context:` or `(A)` to `(P)`, in any case and after any indentation or hidden
  characters;
- caps the length in runes when asked.

## Paths: `PathText`

Run before any path is resolved (`SanitizePath`, `SanitizeNewPath`, and so every
tool that takes a path). A path is refused when it is empty (where a value is
required), longer than 4096 bytes, not valid UTF-8, or contains NUL, any
control character (newline, carriage return and tab included), a C1 control, a
bidirectional or invisible character, or a line separator. The path is never
rewritten. Confinement to the session roots (symlinks resolved, `..` refused)
stays the last word; this only rejects strings that can make two components
disagree about which file is meant.

## URLs: `netguard`

`CheckURL` refuses, never repairs, and returns the URL with a canonical host.
WebFetch requests the canonical form, so the destination that was judged is the
destination that is contacted.

Both profiles refuse: empty or over 8192 bytes, invalid UTF-8, any space,
control character or backslash, credentials in the URL, a missing host, port
0 or more than five digits, a host containing an escape or a zone identifier,
non-canonical numeric hosts (`2130706433`, `0x7f.1`, `127.1`, `0177.0.0.1`,
which a resolver may turn into an address), a host with invisible characters
(other non-ASCII hosts are converted to punycode), malformed percent escapes,
and percent escapes that decode to a control character at any depth up to four
(`%0d%0a`, `%250d%250a`, `%2500`).

| Profile | Scheme | Host |
| --- | --- | --- |
| `Fetch` (model-supplied) | `http` or `https` | Must be public: internal names (`localhost`, `*.localhost`, `*.local`, `*.internal`, `*.intranet`, `*.localdomain`, `*.lan`, `*.home.arpa`, single-label names) and any address in a forbidden range are refused |
| `Endpoint` (user-configured) | `https`, or `http` to a loopback host | Any host, private addresses included, because self-hosted services live there |

`Forbidden` covers, after unmapping IPv4-mapped IPv6: `0/8`, `10/8`,
`100.64/10` (carrier-grade NAT), `127/8`, `169.254/16` (link-local and cloud
metadata), `172.16/12`, `192.0.0/24`, `192.0.2/24`, `192.88.99/24`,
`192.168/16`, `198.18/15`, `198.51.100/24`, `203.0.113/24`, multicast,
`240/4`; and for IPv6 `::`, `::1`, NAT64 (`64:ff9b::/96`, `64:ff9b:1::/48`),
`100::/64`, Teredo (`2001::/32`), documentation (`2001:db8::/32`), 6to4
(`2002::/16`), unique-local (`fc00::/7`, which contains the `fd00:ec2::254`
metadata address), link-local, site-local and multicast. The IPv6 transition
ranges that embed an IPv4 address are refused whole rather than parsed.

The dialer applies `Forbidden` to every address a name resolves to and dials
those exact addresses, so a DNS answer cannot change between the check and the
connection. Every redirect target goes through `CheckURL` again. The four
earlier copies of the loopback test all call `IsLoopbackHost`, which accepts
`localhost` (with a trailing dot, or as a subdomain), loopback literals and
IPv4-mapped IPv6 loopback.

Limit: the IDNA conversion rejects invisible characters and invalid names but
cannot detect look-alike letters from different scripts.

## Headers: `Header`

A header value is refused when it contains anything but printable ASCII, space
or tab: a carriage return, line feed, NUL, other control character, DEL or any
non-ASCII byte. Refusing, not stripping, is deliberate: a stripped `\r\n` would
leave the injected header's text behind.

## Shell commands: `shellsafe`

The harness used to judge a command by splitting on whitespace and rejecting a
few characters. That could not see quoting, wrappers or nesting. `shellsafe`
parses the line with a real bash grammar (`mvdan.cc/sh`) and answers three
questions.

**What commands does the line contain?** `Analyze` returns an `Analysis` (`Analysis.Has` tests its flags) listing every simple command
with quotes removed and expansions kept as source text, in source order, then
the commands found inside wrappers. `Command.Joined` renders a command as one
space-separated string, `Analysis.Subjects` lists them all,
`Analysis.DenySubjects` adds the normalised forms a deny rule needs, and
`Analysis.Simple` says whether the line is one plain command. It unwraps `env`, `command`, `builtin`,
`exec`, `nohup`, `nice`, `ionice`, `setsid`, `stdbuf`, `timeout`, `sudo`,
`doas`, `xargs`, `flock`, `time`, `watch`, `busybox`, `unbuffer`, shells given `-c`
(including `-lc`), `eval`, and the commands run by `find -exec`, `-execdir`,
`-ok` and `-okdir`, to a depth of three. A payload it cannot see into (`env -S`,
a nested payload that does not parse, deeper nesting) sets `FlagOpaque`.

The analysis also reports flags for what the shell would act on: chaining
(`;`, `&&`, `||`, `|`, `|&`, or more than one statement), background, subshell,
block, command and process substitution, redirection (and separately a
redirection that can write a file), here-documents, parameter, arithmetic and
ANSI-C expansion, leading assignments, compound constructs, raw line breaks
(including NEL, U+2028 and U+2029), dynamic words and unquoted globs. A line
that is not valid bash, is over 64 KiB, or contains a NUL or other control byte
is `Parseable == false`.

**Permission rules.** For the `Bash` tool the permission layer no longer matches
the raw string alone.

- A **deny** rule fires if it matches the line as written or any command in it:
  quotes removed, the program named by its base (`/usr/bin/git` as `git`), git,
  gh and glab without their global options (`git -c x=y push` as `git push`),
  and inside every wrapper. If the line does not parse, its separator-split
  segments are matched instead. `"git" push`, `git  push`, `env git push` and
  `sh -c 'git push'` are all `git push` to a deny rule.
- An **allow** rule approves the line only if every command in it is matched by
  an allow rule and the line as written also matches one. `Bash(git status*)`
  therefore no longer approves `git status; rm -rf x`. A line that does not
  parse, has an opaque payload, or writes through a redirection is never
  approved by a rule and falls through to the ask.
- An **ask** rule fires on the line or on any command in it.
- A bare `Bash` rule still covers the tool as a whole.

**Read-only Bash.** `ReadOnly` is used for plan mode, the `read_only` setting,
explore surfaces and the `Git` tool. The line must be exactly one plain command:
parsed, one command, no chaining, redirection, substitution, expansion,
assignment, background or line break. The program must be a bare name from the
allowlist, or an absolute path in `/bin`, `/usr/bin`, `/usr/local/bin`,
`/sbin`, `/usr/sbin` or `/opt/homebrew/bin` (never `./cat` or `/tmp/x/cat`). The
argv that was judged is the argv that runs, with no second split.

Flags that write a file or run a program are refused per program: `sort`
`-o`, `--output` and `--compress-program`; `shuf` and `tree` `-o`; `rg`
`--pre`, `--pre-glob`, `--hostname-bin`; `fd` `-x`, `-X`, `--exec`,
`--exec-batch`; `date -s` and a date positional; `file -C`; a second operand for
`uniq` and `xxd`; `gunzip` without `-c`, `-t` or `-l`; `find`'s `-exec`,
`-execdir`, `-ok`, `-okdir`, `-delete`, `-fprint`, `-fprint0`, `-fls`,
`-fprintf`; and `env` with a command, `-u`, `-S`, `-C`. Long options match on
any unambiguous abbreviation (`--out` for `--output`), and short options are
checked inside clusters (`-uo`).

For git only the read subcommands (`status`, `log`, `diff`, `show`,
`rev-parse`, `ls-files`, `grep`, `describe`) run, and:

- global options `-c`, `--config-env`, `--exec-path`, `-p`, `--paginate`,
  `--html-path`, `--man-path`, `--info-path`, `--super-prefix` and
  `--attr-source` are refused, because they run programs or change
  configuration; `-C`, `--git-dir`, `--work-tree` and `--namespace` are kept;
- `--output`, `--ext-diff`, `--textconv`, `--open-files-in-pager` and
  `grep -O` are refused;
- the argv that runs is hardened by the harness: `-c core.fsmonitor=false -c
  core.pager=cat` is added, and `--no-ext-diff --no-textconv` for `diff`, `log`
  and `show`, so repository configuration cannot name a program to run.

**Rewrite.** The user's `bash_rewrite` table ([Bash rewrite](bash-rewrite.md))
is applied by `Rewrite`, which edits only the program words of commands the line
writes in command position, found by parsing. `ValidRewriteRule` restricts both
sides of a rule to plain words. The result is parsed again and must have the
same flags and command count, or the line is left as sent; the permission rules
then judge the rewritten line in full, and an explicit deny on the original
line stops the call before any rewrite.

Full-mode Bash still runs through `sh -c`; the OS sandbox is its boundary.
`shellsafe` decides permission and the read-only classification, not what the
shell may do once it is allowed.

## Tool arguments: `CheckArgs` and `Format`

`CheckArgs` refuses undeclared keys and then checks each declared value:

| Check | Rule |
| --- | --- |
| Type `string` | Must be a string |
| Type `integer` | A whole number, or a string that parses as one |
| Type `number` | A number, or a string that parses as one |
| Type `boolean` | A boolean, or the string `true` or `false` |
| Type `array` and `object` | Must be an array or an object |
| `Enum` | A non-empty string must be one of the listed values |
| `Format` | A non-empty string must pass the check for its format |

A missing argument is left to the tool, and an empty string passes a format
check. The `Format` is not sent to the model. Formats:

| Format | Check |
| --- | --- |
| `path` | `PathText` |
| `url` | `CheckURL` with the `Fetch` profile |
| `ident` | Only letters, digits, dot, underscore and hyphen |
| `line` | One line, unchanged by `Text` |
| `command` | Valid UTF-8, at most 64 KiB, no control bytes other than tab and line breaks (`shellsafe.Clean`) |
| `glob`, `regex` | Valid UTF-8, no NUL, at most 4096 bytes |

Read, Write, Edit, Glob, Grep and Cd declare `path`, WebFetch `url`, Bash
`command`, Grep's pattern `regex`. An undeclared format fails closed.

## Profile facts: `factspec`

A profile's `facts` ([Facts](agent-profiles.md#facts)) are text the author wrote that
reaches the model and, for the well-known keys, a tool's environment and argv.
`factspec.Validate` refuses a key that is not lowercase letters, digits and
underscores, a key that names a secret (`_secret`, `_token`, `_password`,
`_passphrase`, `_api_key`, `_credentials`), a value that is not one clean line
(unchanged by `sanitize.Text`, at most 512 characters), a value shaped like an AWS
access key id, and a well-known key whose value does not fit its shape. It
refuses rather than repairs, so a value is read as it was written.

`factspec.Bind` is the other half. It maps a fact to a fixed environment variable
or flag chosen from the harness's table, never from a model argument. It also
refuses, for the tools it names, the flags that carry credentials to another
endpoint or identity (`--profile`, `--endpoint-url`, `--ca-bundle`,
`--no-verify-ssl`, `--no-sign-request`, the kubeconfig, server, token and
impersonation flags, `--impersonate-service-account`, `--access-token-file`), and
the flags a pinned fact would be overridden by. A flag matches in its `--flag=value`
form and as an abbreviation, since a CLI may read either, and a flag after a
bare `--` is an argument to something else. A hidden fact (`aws_external_id`) is
read by the harness and left out of the prompt.

## Testing

Every function above has a table test built from real attack lists, and the
delimiter scrubber, the text cleaner, the URL guard and the shell analyser have
native fuzz targets (`FuzzSanitize`, `FuzzText`, `FuzzForDecision`,
`FuzzPathHeader`, `FuzzCheckURL`, `FuzzAnalyze`). Their properties are:
idempotence, no forbidden character in the output, no harness tag surviving any
fold, a URL that passes never naming a private address or credentials, and a
read-only command never carrying shell syntax. A test keeps this page in step
with the code: every exported function of the three packages must appear here
by name.

Run the fuzz targets for a while with, for example,
`go test -run xxx -fuzz FuzzSanitize -fuzztime 60s ./internal/sanitize`.
