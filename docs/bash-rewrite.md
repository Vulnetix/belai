# Bash rewrite

A rule table in your own settings that changes the command a model sends to
`Bash` before anything judges it, for example `npm` to `pnpm` or `pip` to
`uv pip`. The default is no rules, and nothing changes. No model is asked.

```json
{
  "bash_rewrite": {
    "rules": [
      { "match": "npm install", "replace": "pnpm add" },
      { "match": "npm",         "replace": "pnpm" },
      { "match": "pip",         "replace": "uv pip" }
    ]
  }
}
```

| Key | Meaning |
| --- | --- |
| `bash_rewrite.rules` | Ordered list of `match` and `replace`, each one or more space-separated plain words. At most 64 rules. The first rule that matches a command wins for that command. |
| `bash_rewrite.enabled` | `false` keeps the table but turns it off. Default on. |

## How a command is matched

The line is parsed with a shell grammar (`internal/shellsafe`), never searched
as text. A rule applies to a simple command that the line writes itself, when
its argv, after quote removal, starts with the `match` words and each of those
words is a fixed literal (no `$VAR`, no glob). The program word matches by
exact text, or by base name when the rule names a bare command, so `npm` also
matches `/usr/bin/npm` and `"npm"`. Every such command in a chain, pipeline,
group or substitution is rewritten; only the matched words change, so the
rest of the line (flags, redirections, assignments) stays as written.

These are not rewritten: `echo npm`, `grep npm file`, a here-document body, a
quoted string, a command held by a wrapper (`env npm`, `sudo npm`, `xargs npm`,
`sh -c 'npm i'`) or one whose program word is dynamic. A rewrite is one pass:
its output is not fed back through the table.

Both sides of a rule must be plain words made of letters, digits and
`. _ - / @ + : = ,`. The first word of each side must be a command name (not an
option and not `NAME=value`), and the sides must differ. A replacement can
therefore never carry shell syntax. A table that breaks these rules fails
settings validation, naming the rule.

## Permissions see the rewritten line

The rewrite runs once, before permission matching, so the permission rules, the
ask prompt, hooks, the OS sandbox and the executor all see the line that runs.

- The rewritten line is parsed again. If it does not parse, or its shape
  (commands written, chaining, redirection, substitution and so on) differs
  from the line it came from, the call runs exactly as the model sent it.
- The rewritten line is checked against **every** deny, block, allow and ask
  rule, command by command, exactly like any other line: a deny fires on any
  command in it, an allow rule must cover all of them. An allow rule written for
  `npm test` does not approve `pnpm test`.
- A rewrite never launders a denied command. If an explicit deny or block rule
  already matches the line the model sent, the call is left as sent and refused
  by the ordinary permission path; the rule table is not consulted.

## What the model is told

The result begins with a harness-composed note naming the rules that fired and
the line that is now being run (cleaned to one capped line), so the model
knows the command it sent is not the command that ran:

```
[harness: the user's Bash rewrite rules changed your command before it was checked and run (`npm -> pnpm`); the command is now: `pnpm install`]
```

The note carries the user's validated rule words and the line, nothing else.

## Where it is read from

`bash_rewrite` is read from the user's own settings layers (global, then the
user's local layer) only. A repository-visible project layer may set
`bash_rewrite.enabled` to `false` and can never supply a rule or turn the table
on, because a rule decides what runs on your machine. Later user layers replace
the rules as a whole.

See [Sanitization](sanitization.md#shell-commands-shellsafe) for the parser and
[Settings](settings.md) for the layers.
