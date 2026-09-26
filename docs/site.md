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

hero · trust · classifier · sealed · beliefs · labs · modes · tools · diagnostics · permissions · agents ·
memory · processes · budgets · providers · routing · vulnetix · sandbox · extend ·
integrations · cli · qol · start

The sandbox, extend and integrations sections live in
`site/src/components/sections/Extend.astro`. Each card links to the matching
doc under `docs/` on GitHub, so the site states the rule and the doc carries
the edge cases.

Interactive islands live in `site/src/components/ui/` (copy button, comparison
table, shot carousel); everything else ships zero JS.

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
`just site-build`, `just site-check`, `just shots`.