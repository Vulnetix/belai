# Hazard rails

**Status:** new.

A hazard rail is a floor under the permission rules. A command or a file change
that matches one always asks a person, whatever allow rule, mode, hook or ask
setting would otherwise let it run. The harness decides this from fixed tables
and a parsed command line. No model is asked whether a call is dangerous, and no
model can approve one.

Deny rules, blocking hooks, the OS sandbox and the confinement roots still apply
on either side of the ask: a deny rule or a hook deny stops the call before any
ask, and an approved call still runs inside the sandbox and the roots.

## What counts as a hazard

### Shell commands

A `Bash` line is parsed with the shell grammar the permission rules use
(`shellsafe.Analyze`), so quoting, `env`, `sudo`, `xargs`, `sh -c`, `eval` and
`find -exec` are seen through. `shellsafe.Hazards` reads the commands that parse
found and returns identifiers from this closed set:

| Id | The line contains |
| --- | --- |
| `exec.delete.catastrophic` | a recursive `rm` of `/`, `~`, `$HOME`, a top-level system directory (usr, etc, var, home and the like), or the working directory (`.`, `..`, `*`); an `rm` of `/*` or `~/*`; `find` on such a root with `-delete` |
| `exec.disk.raw` | `dd` with `of=` a device other than `/dev/null`, `/dev/zero` and the other harmless ones; `mkfs`, `mkfs.*`, `mke2fs`, `mkswap`, `wipefs`, `fdisk`, `sfdisk`, `cfdisk`, `parted`, `gdisk`, `sgdisk`, `blkdiscard`; `shred` or `tee` aimed at a device |
| `exec.fetch_exec` | a download piped into an interpreter that reads standard input (`curl ... \| sh`), an interpreter given `<(curl ...)`, or `eval` or a shell `-c` given `$(curl ...)`; the downloaders are `curl`, `wget`, `fetch`, `aria2c`, `http`, `https` and `xh` |
| `exec.privilege` | `sudo`, `doas`, `su`, `pkexec`, `runuser`, `setcap`, or a `chmod` that sets a setuid or setgid bit |
| `exec.git.destructive` | `git push` with a force, delete or mirror option, a `+` or `:` refspec; `git reset --hard` or `--merge`; `git clean` with force; `git checkout`, `restore` or `switch` that forces or targets `.`; `git branch -D`; `git stash drop` or `clear`; `git reflog expire` or `delete`; `git gc --prune`; `git prune`; `git filter-branch`; `git filter-repo`; `git update-ref -d` |
| `hazard.opaque` | a line that does not parse, is longer than 64 KiB, holds a control byte, nests a shell payload more than three deep or one the unwrapper cannot see into (`env -S`), names its program through an expansion (`$CMD arg`), gives a recursive `rm` or a `dd of=` a computed target, or gives `git reset` or `git clean` a computed argument or `git` a computed subcommand |

A line that trips several returns several ids, in the order above.

What the parse cannot see, it does not guess. A redirection (`cat x > /dev/sda`),
a script's own contents (`bash run.sh`), a file that is downloaded and run in a
later command, and a dynamic argument to `git push` are not hazards by
themselves. They stay under the ordinary rules, the sandbox and the classifier.

A read-only `Bash` (plan mode, `read_only`) cannot run any of these, so it has no
hazards.

### Files

`Write`, `Edit` and `apply_patch` name the absolute, symlink-resolved file they
would change, by the same resolver the tool uses to confine it. A path that does
not resolve (`../` out of the roots) has no target, and the tool refuses it as
before. Each file is matched against a fixed table and your own additions:

| Id | Class | Matches |
| --- | --- | --- |
| `path.vcs` | repository control | any `.git` or `.husky` segment; `.gitmodules` |
| `path.state` | Belai's own state | any `.belai` segment; `.vulnetix/belai` |
| `path.credential` | credentials and keys | under your home: `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker/config.json`, `.config/gcloud`; by file name anywhere: `.env` and `.env.*` (not `.example`, `.sample`, `.template`, `.dist`), `.netrc`, `.npmrc`, `.pypirc`, `.htpasswd`, `.pgpass`, `credentials`, `credentials.json`, `secrets.json`, `secrets.yaml`, `secrets.yml`, `secrets.env`, `id_rsa*`, `id_ed25519*`, `id_ecdsa*`, `id_dsa*`, and `.pem`, `.key`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, `.ppk` files |
| `path.shell` | shell start-up files | in your home: `.bashrc`, `.bash_profile`, `.bash_login`, `.profile`, `.zshrc`, `.zshenv`, `.zprofile`, `.config/fish/config.fish` |
| `path.lockfile` | dependency lockfiles | `package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`, `pnpm-lock.yaml`, `bun.lock`, `bun.lockb`, `Cargo.lock`, `go.sum`, `poetry.lock`, `Pipfile.lock`, `uv.lock`, `Gemfile.lock`, `composer.lock`, `Package.resolved` |
| `path.ci` | build and release pipelines | `.github/workflows`, `.github/actions`, `.circleci`, `.buildkite`, `.gitlab-ci.yml`, `Jenkinsfile`, `azure-pipelines.yml`, `.travis.yml`, `bitbucket-pipelines.yml`, `.drone.yml` |
| `path.user` | your additions | each `permissions.protected_paths` entry |

A source file whose name only starts like a credential (`credentials.go`) is an
ordinary file. The table is in code (`tools.ProtectedRules`) and a test keeps this
page in step with it.

## How it asks

The ask is the ordinary permission ask with three differences.

- **Nothing skips it.** An allow rule, a mode, a hook allow, an `ask` setting
  and ask off (`/yolo`, `-dangerously-yolo-everything`) do not skip it. Only
  `permissions.hazard_yolo_waive` does, and only while ask is off.
- **Only a person answers.** The decision model that can answer an ordinary ask
  above your bar is not consulted, so it can neither allow nor refuse a hazard
  call.
- **Nothing is remembered.** The ask offers *allow once* and *deny*. A client
  that sends *allow always* anyway gets one approval: the TUI writes no rule, the
  web ask and an ACP session keep no grant, and a spoken keyword does the same.
  A grant the editor or the web user made earlier for the tool does not cover a
  hazard call.

Without anyone to ask (headless, a background agent, a subagent, a worker, a
web session with answers off) the call is withheld, naming the ids, whatever
`permission_ask_no_tty` says. A fleet worker started with ask off, which
normally resolves asks to allow, is withheld too unless you set the waive key.
Turning ask off from the TUI or the web while a hazard ask is open does not
answer it.

Calls a script makes in code mode go through the same path as direct calls, so a
nested `Write` to a workflow file asks too.

## Settings

All three keys live in the `permissions` block beside `allow`, `ask` and `deny`.

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `permissions.hazard_ask` | boolean | `true` | Keep the floor. `false` removes it. |
| `permissions.hazard_yolo_waive` | boolean | `false` | Let ask off skip the floor. |
| `permissions.protected_paths` | list of strings | empty | Paths added to the fixed file table. At most 64, each at most 200 bytes. |

A `protected_paths` entry is one line of plain text with no `..` segment. `~/x`
and an absolute path match that path and anything under it. An entry with a slash
(`vault/keys`) matches that path from any root. An entry without one (`secrets`,
`*.tfstate`) matches a file or directory of that name, and may use `*` and `?`.
You can only add; no entry removes a fixed row.

### Which layer may set what

| Key | Your own layers | A project settings file |
| --- | --- | --- |
| `hazard_ask` | either way | ignored |
| `hazard_yolo_waive` | either way | ignored |
| `protected_paths` | adds | adds |

A project file that sets `hazard_ask` to false or `hazard_yolo_waive` to true is
ignored, and the load adds a note saying so. `/yolo` and the guardrails and ask
switches never set the waive key. A value that is not a boolean, or an entry that
is not plain text, fails the settings load like any other bad key, so the floor is
never off by accident: an absent key is on.

## `/hazards`

`/hazards` prints whether the floor is on, whether ask off waives it, every id
above, the fixed paths per class and your own additions. It is read-only, runs no
model and sends nothing anywhere.

## What is recorded

- **The ask** shows the tool, the cleaned subject (a command line or a path
  reduced to one line) and the ids.
- **The activity feed** gets a `hazard_ask` row (`asked`, `approved`, `denied`
  or `withheld`) with the tool name and the number of ids. It holds no command and
  no path. It shows at the `decisions` level of the internal-work feed.
- **The audit log** gets `hazard.asked` and `hazard.denied` ([audit](audit.md)),
  with the ids joined by `+` in `id` and the tool name in `tool`. Neither carries
  the command or the path.

No classifier kind is involved: nothing arbitrary is read back from the ask.

## Surfaces

| Surface | Behaviour |
| --- | --- |
| TUI | asks; allow once or deny |
| ACP | asks the editor with `allow_once` and `reject_once`; a stored `allow_always` for the tool is ignored for a hazard call |
| `belai rc` with web answers | asks on the web with `allow_once` and `deny`; with ask off and no web answers, denied |
| Headless, `belai agent`, subagents, background agents | withheld, unless a worker was started with ask off and you set `hazard_yolo_waive` |

## Tests

`internal/shellsafe/hazard_test.go` pins the shell table, including wrappers,
substitution and the fail-closed cases. `internal/tools/hazard_test.go` pins the
file table, symlinks and `../`. `internal/agent/hazard_test.go` pins the ask: an
allow rule and ask off do not skip it, the waive, no TTY, the decision model not
called, deny rules, a code-mode call and the audit text. `internal/config`
covers the layers. `TestHazardPageListsEveryId` keeps this page equal to the
tables.
