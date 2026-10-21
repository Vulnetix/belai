# Firewall permissions

People often state permissions in plain words: "never run Bash in this repo",
"don't send customer emails to the model", "only use the GitHub MCP". When a
prompt states a permission the Vulnetix AI Firewall can enforce, belai writes
guardrail rules for it. It shows them on a card in the thread, and applies them
to your organisation's AI Firewall only when you click **Apply**.

## When it runs

- Only while the Vulnetix CLI is signed in. Before the startup probe answers,
  belai reads the CLI's local credentials instead. Signed out, nothing happens:
  no model is asked and no card appears.
- Whether or not `firewall.enabled` is on. A rule is org policy, written through
  the Vulnetix CLI; it does not route this session's traffic.
- Only on a prompt you typed that the role manager admitted. It never runs on a
  refused prompt, a fleet card, a teleport replay, a subagent, steering, a
  clarify answer, a slash command or a `!` shell line. Headless, ACP and fleet
  sessions do not run it.
- Off the turn's path. The pass reads a copy of the prompt in its own goroutine,
  with its own two-minute deadline. The turn never waits on it, and cancelling
  the turn does not cancel it.

## How a rule is written

1. **A word gate.** A prompt with no permission-like word (never, don't,
   allow, deny, only use, redact, mask, PII, secret, …) costs nothing and stops
   here.
2. **Detect: `fwperm_detect`.** A SystemOne decision backend answers one noul
   question: does the message state a rule a network AI firewall could enforce?
   At or above the security deny cut-off the answer is yes; at or below the
   allow cut-off it is no. If no decision backend is set, the endpoint does not
   answer, or the score falls between the cut-offs, the fast-tier model answers
   one word instead (`PERMISSIONS` or `NONE`).
3. **Read the live rules.** `vulnetix ai-firewall get -o json` lists the org's
   guardrails. If it fails, the pass stops and nothing is offered.
4. **Write: `fwperm_write`.** The main model writes the rules as calls to one
   tool, `vulnetix_ai_firewall_guardrail`. It is given the live rules so that
   it does not repeat one already in force.
5. **Validate.** The calls are model output, and every field is checked
   against the firewall's own grammar. A call that fails any check is dropped:
   - `ruleType` must be a content rule (`blocked_pattern`, `pii_redact`,
     `max_messages`) or a capability rule (`tool_`, `mcp_`, `skill_` or
     `client_` with `allow` or `deny`). `delimiter_integrity` is never offered.
   - The action must suit the rule: `strip` only for tool and MCP rules,
     `redact` only for `pii_redact`.
   - Content patterns must compile as RE2, and `max_messages` must be an
     integer from 1 to 10,000.
   - Capability patterns are globs of name characters, where `*` is the only
     wildcard.
   - Names are slugged and prefixed `belai-`.
   - An offer holds at most 12 rules. Duplicate rules, and rules a live
     guardrail already enforces (same type, pattern and action), are dropped.

## Allow and deny variants

A capability family (tools, MCP servers, skills or clients) is in denylist mode
until any enabled `*_allow` rule exists. From then on it is in allowlist mode,
where everything not allowed is refused. A deny always wins. So belai writes
each capability permission as both variants where both can be expressed, in one
group. Both are created; only the one that fits the firewall's mode at the
moment of applying is switched on (`--enable`), and the other is created with
`--disable`:

| What you said | Stance | Switched on |
| --- | --- | --- |
| "never use Bash" | `restrict` | the deny variant (`tool_deny Bash`) |
| "only use Read and Grep" | `only` | the allow variants: allowlist mode is what you asked for |
| "you may use WebFetch" | `grant` | the allow variant if the family is already in allowlist mode; otherwise nothing is switched on, because nothing is denied |

Content rules have one form and are always switched on. A variant that cannot
be expressed is left out ("never use Bash" has no allowlist form), and the
card says so in a note. The choice is made again when you click Apply, against
the rules live at that moment.

## The card

The card uses `ColorPolicy`. It shows the summary, then one line per rule: its
type, action, pattern, and which variant it is (on or off). Under each rule is
the intent in your words, and under all the rules is the first Vulnetix CLI
command it runs.

- **`[ apply ]`** on a rule, or **`[ apply all ]`**, runs
  `vulnetix ai-firewall policy guardrail <name> --rule-type … --action …
  --pattern=… --priority … --enable|--disable -o json` once per rule. Nothing
  passes through a shell. Each rule then shows `✓` applied or `✗` with the
  reason (for example, guardrails need the Teams plan).
- **`[ dismiss ]`** closes the card on this screen. Nothing is written.
- If you do nothing, nothing happens.

An offer that arrives during a turn is recorded at once, and its card is shown
when the turn ends, so a card never lands inside a streaming reply. A resumed
session redraws its cards, with what was applied since.

## Settings

| Setting | Row | Default | What it does |
| --- | --- | --- | --- |
| `firewall.permission_offers` | `permission offers` | on | Off hides the card. The offer is still written to the session, and the console still offers it. |
| `firewall.permission_auto_apply` | `auto-apply permissions` | off | On applies each offer as soon as it is written, with the same variant choice. The card reads "applied automatically". |

Both are written to your global settings. A project layer may set either one
to `false`, never to `true`, so a cloned repository cannot make belai write org
policy without a click.

## What is recorded

Each offer is a `firewall_permission` session entry, and each rule applied (or
failed) is a `firewall_permission_applied` entry. The fields are listed in
[session-sync.md](session-sync.md#what-the-session-records). In a Pix Sandbox,
the console's **Permissions** tab lists the org's live guardrails and every
offer from that sandbox's sessions, and applies them there. AI Firewall rules
are org-wide: applying one from a sandbox applies it to every session in the
organisation.

## What never happens

- Nothing reaches the firewall without a click, unless auto-apply is on.
- The prompt goes to models only through `fwperm_detect` and `fwperm_write`,
  after admission. It is never sent to telemetry.
- The card is a render-only row. No model sees it.
