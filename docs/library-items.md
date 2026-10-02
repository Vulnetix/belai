| `order` | optional whole number from 0 to 999, one to three digits (leading zeros allowed); 0 or absent means "after the last" |# Library items

The Vulnetix website keeps a library for each account: the things worth keeping
when a machine is wiped or a second one is set up. Agent profiles and crews were
the first ([remote-control.md](remote-control.md#agent-library)). A **library
item** is the same idea for the rest of what a host holds: skills, prompts,
supervised processes, repositories, token budgets, provider sets and the Bash
rewrite table.

Every item is one small document. The website stores each version of it (a
version is write-once), lists the hosts that hold it, and can install it on
another connected host or take a backup from one. The host keeps working
offline; the library is a copy, never the thing that is read at run time.

- [Kinds](#kinds)
- [Documents](#documents)
- [Skills](#skills)
- [Prompts](#prompts)
- [Processes](#processes)
- [Repositories](#repositories)
- [How a document travels](#how-a-document-travels)
- [Settings](#settings)
- [Commands](#commands)
- [Security rules](#security-rules)
- [Edge cases](#edge-cases)
- [Server side](#server-side)

## Kinds

| Kind | Command | Document | Lives on the host in | Setting |
| --- | --- | --- | --- | --- |
| `skill` | `belai skill` | Markdown with front matter | `~/.vulnetix/belai/skills/<name>/SKILL.md` | `sync.skills` |
| `prompt` | `belai prompt` | Markdown with front matter | `~/.vulnetix/belai/prompts/<NNN>-<name>.md` | `sync.prompts` |
| `process` | `belai process` | JSON object | `~/.vulnetix/belai/processes/<NNN>-<name>.json` (or a legacy `.sh`) | `sync.processes` |
| `repo` | `belai repo` | JSON object | the `repos` list of `~/.vulnetix/belai/settings.json`; clones under `~/.vulnetix/belai/repos/<dir>` | `sync.repos` |

An item is identified by its kind and its name. On the website each item also
has a uuid; across hosts the kind and name are what match, so the same skill on
two machines is one item.

An account holds at most 200 live items of one kind. A host that holds more of a
kind offers the first 200 by name to the automatic sync and says nothing about
the rest.

## Documents

Two formats, one per kind.

- **Markdown** (skills, prompts): a front-matter block, then the body.
- **JSON** (the other kinds): one object.

The library stores **canonical bytes**, and the SHA-256 of those bytes is what a
sync compares, so the host and the server must agree on them exactly:

- A Markdown document: not empty, valid UTF-8, with no control character other
  than tab and line feed (a NUL, a lone carriage return and a terminal escape are
  refused). `CRLF` becomes `LF`, every trailing whitespace character
  (`unicode.IsSpace`) of the whole document is dropped, and exactly one `\n`
  follows.
- A JSON document: valid UTF-8, with no key repeated inside one object and no
  nesting past 8 levels. It is decoded with numbers kept as written, then written
  again compactly with HTML escaping off, object keys sorted, and one `\n` after
  it. The top level must be an object.

So two spellings of one document have one hash, and a document is never altered
beyond that: a refused document is refused whole, never repaired. The size limit
below applies to the canonical bytes, and a document more than twice the limit
(plus 1 KiB) is refused before it is parsed.

**Names.** A name is 1 to 64 characters: lowercase letters, digits, `.`, `_` and
`-`, starting with a letter or digit (`^[a-z0-9][a-z0-9._-]{0,63}$`). The Bash
rewrite table is the one exception and is always named `bash_rewrite`.

**Limits.** A skill or a prompt document is at most 32 KiB. A process, repository,
budget or rewrite document is at most 16 KiB; a provider document is at most
32 KiB. A document over its limit is refused before it is parsed further.

**Strictness.** A key a kind does not declare is refused, spelled exactly (a key
in the wrong case is unknown), and so is a field of the wrong type, `null`
included. So a document written for a newer Belai is refused by an older one
rather than half applied.

**One set of rules.** The validators in `internal/libitem` apply the rules the
library's server applies (`vdb-site` `belai_items_validate*.go` is the reference)
and the website mirrors for inline errors, so a document one accepts the others
accept. A kind that becomes part of Belai's own settings is also checked by Belai's
own validators (`internal/config`, `internal/skills`) before anything is written,
so a document the library accepts is never written as a setting Belai then refuses
to load; that can only make the host stricter, and only where Belai itself would
refuse the result. The validators are pure, shared by the daemon and the
`belai <kind>` commands, and held to the numbers on this page by tests.

## Skills

A skill document is a `SKILL.md`: the format [skills.md](skills.md#skill-files)
describes, and the loader's own validator (`internal/skills`) is the source of
truth for its front-matter fields. On top of it the library holds these rules:

- The front matter is `key: value` lines between two `---` lines (the closing
  line may have trailing spaces or tabs; a longer line that merely starts with
  `---` is not one). Lines are trimmed, and blank lines and `#` comments are
  skipped. A key given twice and a line that is not `key: value` are refused:
  there is no nested YAML.
- `name` (required) follows the name rule above. `description` (required) is at
  most 300 bytes and one line.
- `license` is at most 128 bytes and `compatibility` at most 500. `metadata` is a
  one-line string of at most 1024 bytes: the loader reads one front-matter line per
  key, so a nested map cannot be expressed and an indented `key: value` under
  `metadata:` would be an unknown key there.
- `allowed-tools` holds at most 64 names of at most 128 bytes, as a one-line list
  `[Bash, "Read"]`. `disable-model-invocation` is `true` or `false`, in any case.
- The body (after the front matter) must not be empty.

On the host a skill is `<skills dir>/<name>/SKILL.md` holding the canonical bytes
exactly, so an installed skill hashes to what the library holds. A skill from a
plugin is `plugin:name` and is never listed, synced or replaced here. A directory
whose `SKILL.md` does not validate, or that repeats a name, is left alone and
reported as skipped by `belai skill list`.

## Prompts

A prompt document is Markdown with these front-matter keys, and nothing else:

| Key | Meaning |
| --- | --- |
| `name` | required; the prompt's name |
| `description` | optional; at most 300 bytes, one line |
| `order` | optional whole number from 0 to 999; 0 or absent means "after the last" |
| `enabled` | optional; `false` disables the prompt (default `true`) |

The body is the prompt text and must not be empty. Only the **global** prompt
library is synced: a project prompt (`.vulnetix/prompts`) is never listed, sent
or written.

The host keeps a prompt as a plain-text file, `<NNN>-<name>.md` (`_<NNN>-<name>.md`
when disabled), holding the prompt text alone. The file name carries the order and
the enabled state, so a prompt's name must be one a file name can hold (lowercase
letters, digits and single hyphens); a library prompt named `my.prompt` is refused
on install with that reason. The description has no place in the file, so the host
keeps it, with the document it installed, in `~/.vulnetix/belai/library/prompts.json`.
While the file still matches what was installed the host exports the installed
document byte for byte; after an edit, a reorder or a disable it composes a fresh
document from the file and keeps the description. That is what stops an install
from looking like an edit on the next sync.

An exported prompt writes `name`, then `description` when it has one, `order` when
it is above 0, and `enabled: false` only when disabled.

## Processes

A process is a command the Belai daemon supervises, run as an argv with **no shell**:
nothing is quoted, split or expanded, so an argument that holds a space or a
semicolon is one argument. The document is a JSON object with exactly these keys:

| Key | Rule |
| --- | --- |
| `name` | required; the name rule above |
| `command` | required; 1 to 1024 bytes, no control character |
| `args` | at most 64 strings of at most 1024 bytes; tab, line feed and carriage return are allowed (a script can be one argument), NUL and other control characters are not |
| `options` | at most 64 of `{name, value?}`; `name` matches `^-{1,2}[A-Za-z0-9][A-Za-z0-9._-]*$` and is at most 128 bytes, `value` is a string of at most 1024 bytes |
| `env` | at most 64 variables; a name matches `^[A-Za-z_][A-Za-z0-9_]*$` (at most 128 bytes), a value is at most 4096 bytes |
| `cwd` | at most 1024 bytes: absolute, `~` or `~/…`, or relative to the project directory |
| `user` | `""` or `^[a-z_][a-z0-9_-]{0,31}$` |
| `stdout`, `stderr` | a redirect, below. Defaults: stdout `log`, stderr `stdout` |
| `enabled` | boolean, default true; the file name carries it (`_NNN-`) |
| `order` | whole number 0 to 999, default 0; the file name carries it (`NNN-`) |

**Argv.** The host runs `command`, then each option's `name` and, when the key
`value` is present (an empty string counts: one empty argument), the value as its
own element, then `args`.

**Redirects.** A redirect is `{"mode", "path"?}`. `mode` is `log` (the process log
and the live tail), `discard`, `file` (truncated at each start), `append`, or
`stdout` (stderr only: merged into stdout). `path` is required for `file` and
`append` and refused for every other mode. A path is `~`, absolute, or relative to
the project directory and may not climb out of it with `..`; the file is created
`0600`, never through a symbolic link, and its directory must exist. Two streams
naming one path share one file.

The library accepts a relative `cwd` or redirect path that uses `..` (it cannot know
the project directory); the host refuses it when the process starts, with the
reason, rather than let a document climb out of the project directory.

**Secrets are never synced as literals.** An `env` name that matches
`(?i)(secret|token|password|passwd|api[_-]?key|credential|private)` must have a
value of the form `env:OTHER`, which means "copy the host's variable `OTHER`" at
start (from the real environment, which the scrubbed child environment would
otherwise drop). A value that starts with `env:` must be exactly that form, for any
name, and the prefix is case-sensitive. The check reads the variable name only:
`args` and option values are stored as written, so a secret must never be put
there. A start whose `env:OTHER` names an unset variable is refused, naming it.

**`user`** is honoured only when Belai runs as root. Anywhere else the process is
refused: it is never started as the account that runs Belai in its place, and no
redirect file is opened for it. As root the process runs with that account's
groups and its `HOME`, `USER` and `LOGNAME` (unless `env` sets them), and a
redirect file is handed to that account.

**The host.** The file is `<NNN>-<name>.json` in the global processes directory,
holding the document (indented; the host re-canonicalises it to hash it). The file
name is authoritative for what it can say: its slug is the name, a `_` prefix is
`enabled: false` and its number is the order, so a hand-edited document that
disagrees is exported with the file name's values. A name must be one a file name
can hold (lowercase letters, digits, single hyphens); a library process named
`my.web` is refused on install with that reason. A document with the same slug as
a legacy file replaces it.

**Legacy files.** `NNN-slug.sh` (the whole file is one `sh -c` string, written by
`!!cmd`) still works. It syncs as `{name, command: "sh", args: ["-c", <body>],
order, enabled}`; a body over 1024 bytes does not fit one argument and is skipped.
When both a `.sh` and a `.json` hold one slug the structured one wins and the
other is a stray. Only the global library is synced: a project process
(`.vulnetix/processes`) is never listed, sent or written.

**Recovery.** When a supervised process exits unexpectedly the recovery subagent may
amend the flags of a shell command a user typed. A structured process restarts
exactly as defined: its argv, environment, user and redirects are not the
subagent's to change. A structured process is not wrapped in `sh`, but runs under
the same OS sandbox as any supervised process, with the project directory as its
only writable root whatever `cwd` says.

## Repositories

A repository item is **deployment configuration** for a git checkout the host keeps.
The library never clones, fetches or contacts the repository, and it never stores a
credential. The document is a JSON object; every key but `installation_id` is
required, and any other key is refused:

| Key | Rule |
| --- | --- |
| `name` | the name rule above |
| `url` | `https://host[:port]/path[.git]` or `git@host:path.git`, at most 1024 bytes, see below |
| `visibility` | `public` or `private` |
| `auth` | `none` or `github_app`. `public` requires `none` and `private` requires `github_app`: a plain token has no place here |
| `installation_id` | whole number 1 to 2^53 - 1; **required** when `visibility` is `private`, **refused** when it is `public` |
| `refs` | 1 to 16 distinct `{kind, name}`; `kind` is `branch`, `tag` or `sha` |
| `dir` | the checkout directory under the repos directory, at most 256 bytes |
| `depth` | whole number 0 to 1000; 0 is the full history |
| `submodules` | boolean |
| `enabled` | boolean |

**URL.** No space, control character or backslash. The `https` form has a host that
starts with a letter or digit (so it cannot be read as an option), an optional port
from 1 to 65535, and a path with at least one segment; it must not carry credentials
(`user@`, `user:pass@`, `token@`), a query or a fragment. The scp form is exactly
`git@host:path.git`. In both, no path segment is empty, `.`, `..` or starts with `-`.
IPv6 literals, `http://`, `ssh://`, `git://`, `file://` and local paths are refused.

**Refs.** A `sha` is 7 to 64 hex characters (either case). A `branch` or `tag` is given
without `refs/heads/` or `refs/tags/`, at most 255 bytes, and follows
`git check-ref-format`: no leading `-` or `/`, no trailing `/`, `.` or `.lock` on a
path component, no component starting with `.`, no `..`, `//` or `@{`, no space or
control character and none of `~ ^ : ? * [` or a backslash. The name `@` alone is
refused. The same `kind` and `name` twice is refused; a branch and a tag of one name
are two refs.

**Dir.** Relative: no leading `/` or `~`, no `:` (a drive), no backslash, and no empty,
`.` or `..` segment.

**On the host** the entry lives in the `repos` list of the user's own
`settings.json` (a project settings file's `repos` is dropped and never read or
written), as the document's keys. A hand-written entry may leave `dir` (default: the
name), `depth` (0), `submodules` (false) and `enabled` (true) out, and the host
exports it with them spelled out, because the library requires them. An install adds
the entry, or replaces the one of that name when told to, and refuses a `dir` another
entry already uses. Nothing is cloned by an install: `belai repo sync` does that.

**`belai repo sync [NAME]`** runs `git` as an argv, never a shell, for each enabled
repository (or the one named), with hooks off, no prompts, the scrubbed environment
and only the https and ssh transports:

- A missing checkout is created at `<repos dir>/<dir>` (`0700`) with the url as
  `origin`; an existing directory that is not a git checkout, or a checkout whose
  `origin` is another url, is refused and left alone. A symbolic link anywhere in the
  path is refused. A first sync that fails removes what it created.
- Every listed ref is fetched. The **first** ref is the one checked out; the rest are
  fetched and kept (`refs/remotes/origin/<branch>`, `refs/tags/<tag>`) but not
  checked out.
- A **branch** is moved by fast-forward only. A branch with local commits ahead of
  origin is left as it is (and says so); a diverged branch is refused, never reset.
- A **tag** or a **commit** is a detached checkout. A tag is fetched as it is and
  never moved: a tag that moved upstream is refused. A commit not on origin is an
  error.
- A checkout with a change to a tracked file is refused before it is moved to another
  ref or fast-forwarded. Untracked files never block a sync and are never touched.
- With `submodules` on, submodules are initialised and updated recursively.
- Two syncs of one repository never run at once (an advisory lock beside the clone).

**Credentials.** A public repository is fetched with every credential helper switched
off. A private repository over `ssh` uses the ssh agent and keys, in batch mode (your
`GIT_SSH_COMMAND` is respected). A private repository over `https` is fetched through
the GitHub CLI's credential helper (`gh auth git-credential`), scoped to that host:
the CLI must be installed and signed in to the host (`gh auth login`), otherwise the
sync fails with that reason, before git runs. Belai has no GitHub App flow of its own,
so `installation_id` is the library's record of which installation the deployment
uses; the host does not send it anywhere. A token is never put in a URL or an
argument; the token variables the CLI reads (`GH_TOKEN`, `GITHUB_TOKEN`, ...) are
passed to it only for a private repository.

**`belai repo status [NAME]`** reports each repository from what the last sync left,
without touching the network: `not cloned`, `current`, `behind N`, `ahead N`,
`diverged`, `other ref` (the checkout is on something other than the first ref),
`not fetched`, `disabled` or `invalid`, and whether a tracked file has local changes.

## How a document travels

Three things move a document between a host and the library. All of them need
`belai rc` running and the Vulnetix CLI signed in, and all of them go through the
same client as session sync (same origin allowlist, same credential).

**Backup (`item_backup`).** The website names a kind and a name. The host exports
that item as its canonical document and uploads it; the website stores it as a new
version. A backup writes nothing here.

**Install (`item_install`).** The website names a library item, one of its
versions, a kind, and whether it may replace the host's item. The host fetches that
version (the server serves it only while the request is delivered to this host)
and writes it, under the rules in [Install rules](#install-rules). The
acknowledgement is harness words with a short cleaned excerpt, never the document.

**Automatic sync.** Every 30 seconds the daemon hashes each local item of each
enabled kind and asks the server what to do with the ones it has not settled. The
answers and what the host does are the same as for agents and crews:

| Answer | Meaning |
| --- | --- |
| `push` | the library has nothing newer, so the host pushes its copy as a new version |
| `current` | the library already holds this copy |
| `diverged` | the website saved a version this host has not installed, so the host's copy never overwrites it |
| `skip` | deleted from the library, not this account's, or a kind or name the server does not take |

The server decides again when the host pushes, so a web edit saved in between is
never lost. A diverged or skipped item is asked about again after ten minutes, a
website that answers not found pauses the whole sync for ten minutes, and an item
the server does not answer at all (a website that predates items) is settled as
skipped for ten minutes rather than asked about every check. After a backup or an
install the host records the item as the library's copy, so neither is taken for an
edit. The state is in memory: a restart asks again.

### Install rules

An install is a person's action on the website, made with their own login, so like
a profile install it does not need `sync.remote_prompts`. It is still held to these
rules, and a refusal writes nothing:

- The kind's setting (`sync.skills`, `sync.prompts`) is on. Otherwise the request is
  refused with that reason, before the library is asked for anything.
- The document passes the kind's validator whole, and its name is the one the
  library item has.
- An item of the same name is replaced only when the request says so. Without it
  the install is refused with "install it again with replace turned on".
- A skill or a prompt is untrusted text, so before it is written it passes the
  sanitisation gate (see [sanitization.md](sanitization.md#library-items)): it is
  refused if it holds harness delimiter markup, a terminal escape or other control
  character, a bidirectional override, or an invisible character that hides text.
- The write is atomic (a temporary file, then a rename), the files are `0600` and a
  new skill directory is `0700`. A symbolic link at the target is refused: an
  install never writes through one.

## Settings

Each kind has a switch under `sync`, default on, and never on while `sync.enabled`
is off. A project settings file may turn one off, never on.

- **`sync.skills`**: keep the skill library current by itself, advertise the host's
  skills, and take `item_backup` and `item_install` requests for skills.
- **`sync.prompts`**: the same for the global prompt library.
- **`sync.repos`**: the same for the `repos` list.
- **`sync.processes`**: the same for the global process library. A synced process
  is a command Belai runs, so this is the switch that keeps the website out of
  what runs on the host: with it off nothing is advertised, pushed or installed.

An off switch removes the kind from all three: nothing is hashed, advertised or
sent, and a request for it is refused with a reason. The settings are described
with the others in [session-sync.md](session-sync.md#settings).

The host also tells the website which items it holds, as kind, name and hash,
never a document, so the library page can say how many hosts hold each item and
which items are in the library on no host. Only the kinds whose switch is on are
advertised.

## Commands

`belai skill`, `belai prompt`, `belai process` and `belai repo` read and write items by hand, with the same
validator and the same install rules as the daemon:

```sh
belai skill list [-json]
belai skill validate FILE
belai skill import [-force] FILE
belai skill export [-force] NAME [FILE]
```

`belai prompt`, `belai process` and `belai repo` take the same four commands. `belai repo`
also has `sync` and `status` (see [Repositories](#repositories)), and its `list` shows
each repository's url, first ref and dir.

`FILE` of `-` is standard input (and, for `export`, standard output, which is also
the default). `list` shows each item's name, size and the first 12 digits of the
hash the library compares, and reports an item it cannot sync (an invalid
document, a repeated name, a file over the limit) on standard error as `skipped`.
`import` refuses to replace an item of the same name without `-force`. `export`
refuses to replace an existing `FILE` without `-force`, and writes it `0600`. The
commands work offline.

The website's pages import and export the same documents: a file chosen there is
checked with the same rules in the browser, then stored as a new version of the
item of that name.

## Security rules

- **A document is data until it validates.** It is canonicalised, size-checked and
  parsed against a closed schema before anything is written. An unknown key, a
  type that does not fit, or a limit exceeded refuses the whole document.
- **Untrusted text is gated.** A skill or a prompt body is text another party may
  have written, so it never reaches the host's files with delimiter markup or
  invisible characters in it (see [sanitization.md](sanitization.md#library-items)).
  Reading one later is unchanged: a skill result is still classified, and a prompt
  is still admitted like any text the user submits.
- **The website cannot widen what a host does without the user's setting.** Every
  kind has its own switch; off means no hash, no advertisement and no install.
- **Only the user's layers.** The project layer is never read or written.
- **Nothing in a refusal repeats the document.** Reasons are harness text and short
  cleaned excerpts.
- **No path comes from a document.** An item is written under its validated name in a
  fixed directory, never at a path the document names. (A process may name a
  `cwd` or a redirect path to use when it runs, which is its own business and is
  checked when it starts.)
- **A process is an argv, never a shell string, and holds no secret.** The library
  refuses a literal under a secret-looking `env` name, a start refuses an unset
  `env:OTHER`, `user` is honoured only as root and never falls back to the current
  user, redirect files are `0600` and never opened through a link, and the OS
- **A repository never carries a credential, and git never runs a hook or a prompt.**
  The library refuses a url with userinfo, a query or a fragment and a `private`
  document with a plain token; the sync runs git as an argv with hooks off, only the
  https and ssh transports, no credential prompt, and a branch moved only by
  fast-forward, so a dirty tree or a diverged branch is refused rather than reset.
  sandbox's writable roots stay the project directory whatever `cwd` says.

## Edge cases

- **A skill and its CRLF twin** are one item: the same bytes, the same hash.
- **A skill edited by hand** after an install is asked about again (its hash
  changed). If the website also moved, it is `diverged` and the host's copy is not
  pushed; back it up or install the website's version to settle it.
- **A prompt renamed on disk** is a different item: the old name is gone from the
  host and the new one is new to the library.
- **A prompt whose body is empty** cannot be a document and is skipped.
- **An item over its size limit** is skipped by the listing and the sync, never
  truncated.
- **Two directories holding one skill name:** the first (by directory name) is the
  item and the second is reported as skipped.
- **A library item with no host.** It stays in the library until deleted; any
  connected host can install it.
- **A host that is offline** still has its items; nothing is lost, and the next
  sync reconciles them.
- **A version of `belai` that predates a kind** refuses its requests with "update
  Belai on the host".

## Server side

- **API:** `vdb-site` (`api/internal/handler/belai_library_items*.go`) serves
  `/v1/belai/library/items/{kind}` for the website, where `{kind}` is the plural
  (`skills`, `prompts`, ...): list, create (upsert by name), versions, rewind,
  delete, and `GET /library/item-names/check`. A host answers a backup with
  `POST /hosts/{id}/library/item-backups` and an install with
  `GET /hosts/{id}/library/items/{kind}/{item}/versions/{version}?dispatch=`; the
  automatic sync extends `POST /hosts/{id}/library/sync` with `items` and takes a
  push on `PUT /hosts/{id}/library/sync/items`.
- **Storage:** a version is one write-once object under
  `belai/{tenant}/items/{kind}/{item}/{YYYYMMDDHHMM}.{md|json}`, holding the
  canonical bytes; the index is `BelaiLibraryItem` and `BelaiLibraryItemVersion`
  in `saas/prisma/models/belai.prisma`, and `BelaiHostLibrarySync` remembers the
  version each host last held.
- **Retention:** the account keeps the newest N versions of every agent, crew and
  item (`PUT /library/settings`, default 3, 1 to 50), never the latest.
- **Website pages:** `src/pages/resolve/belai-skills.vue` and
  `belai-prompts.vue`, in the sidebar's **Belai** group.
