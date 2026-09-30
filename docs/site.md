# Marketing site

The single-scroll marketing site at [belai.vulnetix.com](https://belai.vulnetix.com/), built from `site/`.

## Stack

- Astro + React islands, Tailwind v4 (CSS-first), Yarn 4.
- Geist (self-hosted OTFs in `site/public/fonts/`) and Geist Mono (`@fontsource/geist-mono`).
- Brand tokens ported verbatim from `Vulnetix/website/src/styles/_brand.scss` into
  `site/src/styles/brand.css` as Tailwind `@theme` entries.

## Layout

`site/src/pages/index.astro` composes one long scroller with a sticky left status
rail (≥1120px). The content is full-width (no fixed max-width). Section order:

hero · trust · classifier · sealed · beliefs · labs · modes · tools · diagnostics · permissions · agents & crews ·
memory · processes · budgets · session intelligence · providers · routing · vulnetix · kanban · web sessions · sandbox · extend ·
integrations · cli · qol · start · faq

The agents section (`site/src/components/sections/Agents.astro`, rendered
from `Features.astro`) is one ladder: helpers, background agents, workers and crews.
Three step cards open it, a table (cards below `lg`) says what changes on
each step, then come a crew's recorded run on a board (`#crews`, which the
kanban section links to), the crew fences, the built-in crews and a worker
profile. The step facts live in one `steps` array that feeds both the table
and the cards. Keep the run truthful: it quotes the item history and draft
PR of a real release.

The session intelligence section (`site/src/components/sections/Intel.astro`,
rendered from `Features.astro`, id `intel`) follows budgets. It opens with a
working demo of the `f12` pane on an ink panel: four readouts (limit used,
pace, trend, runway), the 5-hour and weekly limit bars, and a list whose tabs
(`t`, `m`, `r`) and window arrows change what it shows, with short notes under
it that explain each readout. After the demo come the footer in its four colours,
a runway worked through with the numbers from the unit tests, a 7-day heatmap,
the providers that report limits, and the three ways in. The glyph helpers, the
demo data and the list markup live in `site/src/lib/intel.ts`, which the server
render and the client script both import, so the first paint and every later
state come from one place. Its rules mirror `internal/tui/components`
(`fillBar`, `LimitBar`, `Sparkline`) and `site/scripts/intel.test.mjs` pins them
the way the Go tests do: a marker shows only while the fill has not reached it,
and every bar keeps its width. The rules the section states are R15 to R22 in
[Token budgets](token-budgets.md#session-intelligence); change both together.
Without JS the demo shows its first state (this week, timeline).

The web sessions section (`site/src/components/sections/WebSessions.astro`)
covers following and answering a session on the Vulnetix website.

The sandbox, extend and integrations sections live in
`site/src/components/sections/Extend.astro`. Each card links to the matching
doc under `docs/` on GitHub, so the site states the rule and the doc carries
the edge cases.

Interactive islands live in `site/src/components/ui/` (copy button, comparison
table, shot carousel); the session intelligence demo carries a small inline script, and everything else ships zero JS.

## The sealed block

`site/src/components/seal/SealedSection.astro` is the page's signature device.
Each section is framed as a harness block:

```
<beliefs nonce=a264c816 sha256=afa55bb9…>   sealed
  …content…
</beliefs>
```

The digest is a real SHA-256 of `nonce + "\n" + sealText`, computed at build
time. The payload is rendered as a visually-hidden `.seal-payload` span so the
browser can recompute the digest. A tiny inline script (in
`site/src/layouts/Base.astro`) observes each `.sealed` section, recomputes the
digest from the payload actually in the DOM, and flips the badge from `sealed`
to `✓ verified` only on a match. It is the page demonstrating its own trust
model: nothing is trusted on sight.

Edge cases:

- Nonces are deterministic (`sha256("nonce:" + label)` truncated to 8 hex
  chars), so rendered delimiters diff cleanly across builds.
- The payload is single-line; the digest covers `nonce + "\n" + payload`
  exactly, and the browser uses the same framing.
- The badge is `aria-live`-free but the flip is a class change; the visible
  state text is still machine-readable (`sealed` / `✓ verified`).

## Pix poses

`site/src/assets/pix/` holds eight poses: the three canonical poses copied from
the sibling repos (agreeable, contemplative, professor) and five new ones
(sentinel, facepalm, yolo, conductor, homestead). New poses are authored by
copying `pix-professor.svg` and editing only the aria-label, pose keyframes, and
the `<g id="cyberwing">` subtree; the shared defs, clipPaths, body, eyes, visor,
beak, feet and layer order stay byte-identical. Every animated id is listed in
the SVG's `prefers-reduced-motion` block with a static fallback for anything
that starts at `opacity: 0`.

## Editor logos

`site/src/assets/editors/` holds the marks shown in the "Belai in your editor"
block of `Extend.astro`. They are the vendors' own SVGs and appear only to name
the editor they belong to (the one exception to naming things by what they do).
Sources: Zed, `assets/images/zed_logo.svg` in the Zed repository; JetBrains,
`resources.jetbrains.com` brand logos; Neovim, the Neovim mark on Wikimedia
Commons; Emacs, `EmacsIcon.svg` on Wikimedia Commons; VS Code, the Visual Studio
Code icon on Wikimedia Commons. Check each project's brand guidelines before
recolouring or resizing beyond the CSS on the page.

## TUI shot captures

`tools/shot` renders real TUI surfaces headlessly to TrueColor ANSI files in
`site/src/assets/shots/`, and `site/scripts/ansi-to-svg.mjs` converts them to
Geist-Mono SVG. The captures use `internal/tui/components` (never the
interactive `internal/tui` App).

Determinism rules:

- `tools/shot` forces `lipgloss` TrueColor and `HasDarkBackground(true)`, and
  runs with stdout on a pty so `Banner`/`ExitCard` render their colour path.
- No timestamps or random values in any frame.
- The budget frames (`budgets`, and the gauge in `footer`) show a fixed clock —
  09:30 on day 24 of a 30-day month — and every row must obey the colour
  rules in [Token budgets](token-budgets.md) (R7, R8) for that clock.
- The intel frames (`intel`, and `footer-intel`) use fixed numbers, and every
  bar, marker and label must obey the colour and marker rules in
  [Token budgets](token-budgets.md) (R16, R20).
- The half-block glyph `▀` is drawn as two stacked rects; an unset half means
  "no pixel" and falls back to the ink background, so empty pixels read as
  empty rather than as speckles of light.
- `just shots` regenerates every `.ansi` and `.svg`; on a clean tree the diff
  must be empty.

## Terraform

`site/terraform/` owns the Cloudflare CNAME (`cloudflare_dns_record.belai`) and,
gated behind `var.manage_pages = false`, the GitHub Pages block. The Pages block
is net-new for the org and delivered inert: `terraform plan` proposes no
repository change until `manage_pages` is flipped and the repository is imported
first (`terraform import github_repository.belai belai`).

## Deploy

`.github/workflows/pages.yml` builds `site/`, asserts `dist/CNAME` still reads
`belai.vulnetix.com` (a missing CNAME silently unbinds the custom domain), runs
the link checker, then uploads and deploys the Pages artifact.

## Local workflow

See the `# ---- Site ----` recipes in the `justfile`: `just site-dev`,
`just site-build`, `just site-check` (the site script tests, the build and the link check), `just shots`.