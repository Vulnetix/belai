# Library items

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

- A Markdown document: `CRLF` becomes `LF`, every trailing whitespace character
  (`unicode.IsSpace`) of the whole document is dropped, and exactly one `\n`
  follows. It must be valid UTF-8 and hold no NUL byte.
- A JSON document: decoded with numbers kept as written, then written again
  compactly with HTML escaping off, object keys sorted, and one `\n` after it.
  The top level must be an object.

So two spellings of one document have one hash, and a document is never altered
beyond that: a refused document is refused whole, never repaired.

**Names.** A name is 1 to 64 characters: lowercase letters, digits, `.`, `_` and
`-`, starting with a letter or digit (`^[a-z0-9][a-z0-9._-]{0,63}$`). The Bash
rewrite table is the one exception and is always named `bash_rewrite`.

**Limits.** A skill or a prompt document is at most 32 KiB. A process, repository,
budget or rewrite document is at most 16 KiB; a provider document is at most
32 KiB. A document over its limit is refused before it is parsed further.

**Strictness.** A key a kind does not declare is refused, spelled exactly (a key
in the wrong case is unknown). So a document written for a newer Belai is refused
by an older one rather than half applied.

The validators live in `internal/libitem`. They are pure, shared by the daemon
and the `belai <kind>` commands, and held to the numbers on this page by tests.

## Skills

A skill document is a `SKILL.md`: the format [skills.md](skills.md#skill-files)
describes, and the loader's own validator (`internal/skills`) is the source of
truth for its front-matter fields. On top of it the library holds these rules:

- The front matter is `key: value` lines between two `---` lines. A key given
  twice, an indented line, a line that is not `key: value` and a stray `---` line
  are refused: there is no nested YAML.
- `name` (required) follows the name rule above. `description` (required) is at
  most 300 bytes, one line, with no control or invisible character.
- `metadata` is a one-line map, `{team: platform, tier: 2}`, with at most 32 keys.
  The loader reads one front-matter line per key, so a nested block map cannot be
  expressed; a `metadata` that is not a one-line map is refused.
- `allowed-tools` holds at most 64 names. `disable-model-invocation` is `true` or
  `false`. `license` and `compatibility` are free text.
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

An off switch removes the kind from all three: nothing is hashed, advertised or
sent, and a request for it is refused with a reason. The settings are described
with the others in [session-sync.md](session-sync.md#settings).

The host also tells the website which items it holds, as kind, name and hash,
never a document, so the library page can say how many hosts hold each item and
which items are in the library on no host. Only the kinds whose switch is on are
advertised.

## Commands

`belai skill` and `belai prompt` read and write items by hand, with the same
validator and the same install rules as the daemon:

```sh
belai skill list [-json]
belai skill validate FILE
belai skill import [-force] FILE
belai skill export [-force] NAME [FILE]
```

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
- **No path comes from a document.** A skill or prompt is written under its
  validated name in a fixed directory, never at a path the document names.

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
