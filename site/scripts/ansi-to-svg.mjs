#!/usr/bin/env node
// ansi-to-svg.mjs — convert TrueColor ANSI captures into Geist-Mono SVG.
//
// Input:  site/src/assets/shots/*.ansi (produced by tools/shot)
// Output: site/src/assets/shots/*.svg
//
// The TUI draws on a dark terminal, so every capture is rendered on the ink
// background from internal/tui/components/theme.go. SGR colours arrive as
// truecolour sequences (38;2;r;g;b / 48;2;r;g;b) and are emitted verbatim —
// they already *are* the theme.go palette, baked in by lipgloss at render time.
// The half-block glyph U+2580 (▀) is drawn as two stacked rects because a font
// cannot carry the two-tone foreground/background split that makes the Pix owl.

import { readFileSync, writeFileSync, readdirSync, mkdirSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { dirname, join, basename } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const shotsDir = join(__dirname, '..', 'src', 'assets', 'shots');

// theme.go palette (documented; the ANSI already carries these values).
export const palette = {
  ink: '#1C3431',
  teal: '#3AC4B4',
  tealSoft: '#76E0CD',
  cream: '#F6EED6',
  amber: '#E8912B',
  danger: '#E2564E',
  bone: '#E8E7E3',
  // AdaptiveColor Dark variants (the shot tool forces HasDarkBackground=true).
  muted: '#7D918D',
  line: '#2F4340',
};

export const CELL_W = 10;
export const CELL_H = 20;
export const FONT_SIZE = 15;
export const BASELINE = 16; // baseline offset within a cell row

// Terminal defaults on the dark canvas.
export const DEFAULT_FG = palette.bone;
export const DEFAULT_BG = palette.ink;

const SGR_RE = /\x1b\[([0-9;]*)m/g;

function esc(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// sgrDeltas walks one SGR parameter list and yields state deltas. Handles the
// sequences the captures emit: reset, bold, truecolour fg/bg, default fg/bg,
// and reverse video.
export function* sgrDeltas(params) {
  if (params === '') params = '0';
  const codes = params.split(';').map((n) => Number.parseInt(n, 10));
  let i = 0;
  while (i < codes.length) {
    const c = codes[i];
    if (c === 0) yield { type: 'reset' };
    else if (c === 1) yield { type: 'bold', value: true };
    else if (c === 22) yield { type: 'bold', value: false };
    else if (c === 39) yield { type: 'fg', value: null };
    else if (c === 49) yield { type: 'bg', value: null };
    else if (c === 7) yield { type: 'reverse' };
    else if (c === 38 && codes[i + 1] === 2) {
      yield { type: 'fg', value: `rgb(${codes[i + 2]},${codes[i + 3]},${codes[i + 4]})` };
      i += 4;
    } else if (c === 48 && codes[i + 1] === 2) {
      yield { type: 'bg', value: `rgb(${codes[i + 2]},${codes[i + 3]},${codes[i + 4]})` };
      i += 4;
    }
    i++;
  }
}

// parseLine splits a raw ANSI line into styled cells: { ch, fg, bg, bold }.
export function parseLine(line) {
  const cells = [];
  let fg = null;
  let bg = null;
  let bold = false;
  let reversed = false;
  let idx = 0;

  const apply = (delta) => {
    switch (delta.type) {
      case 'reset':
        fg = null;
        bg = null;
        bold = false;
        reversed = false;
        break;
      case 'bold':
        bold = delta.value;
        break;
      case 'fg':
        fg = delta.value;
        break;
      case 'bg':
        bg = delta.value;
        break;
      case 'reverse':
        reversed = !reversed;
        break;
    }
  };

  while (idx < line.length) {
    SGR_RE.lastIndex = idx;
    const m = SGR_RE.exec(line);
    if (m && m.index === idx) {
      for (const delta of sgrDeltas(m[1])) apply(delta);
      idx += m[0].length;
      continue;
    }
    const cp = line.codePointAt(idx);
    const ch = String.fromCodePoint(cp);
    const effFg = reversed ? bg : fg;
    const effBg = reversed ? fg : bg;
    cells.push({ ch, fg: effFg, bg: effBg, bold });
    idx += ch.length;
  }
  return cells;
}

// renderCell renders one cell at column x, row y. Exported for tests.
export function renderCell(cell, x, y) {
  const top = y * CELL_H;
  const out = [];

  if (cell.ch === '\u2580') {
    // upper half block: top half foreground, bottom half background. An unset
    // half means "no pixel" and falls back to the terminal background (ink),
    // not the default text colour, so empty pixels read as empty rather than
    // as speckles of light.
    const topCol = cell.fg ?? DEFAULT_BG;
    const botCol = cell.bg ?? DEFAULT_BG;
    out.push(`<rect x="${x}" y="${top}" width="${CELL_W}" height="${CELL_H / 2}" fill="${topCol}"/>`);
    out.push(`<rect x="${x}" y="${top + CELL_H / 2}" width="${CELL_W}" height="${CELL_H / 2}" fill="${botCol}"/>`);
    return out.join('');
  }

  if (cell.bg) {
    out.push(`<rect x="${x}" y="${top}" width="${CELL_W}" height="${CELL_H}" fill="${cell.bg}"/>`);
  }
  const weight = cell.bold ? '700' : '500';
  const fill = cell.fg ?? DEFAULT_FG;
  out.push(
    `<text x="${x}" y="${top + BASELINE}" font-family="'Geist Mono','DejaVu Sans Mono','Menlo',monospace" font-size="${FONT_SIZE}" font-weight="${weight}" fill="${fill}">${esc(cell.ch)}</text>`,
  );
  return out.join('');
}

// renderAnsi converts an ANSI capture string into a full SVG document. Pure:
// it reads no files, so it is unit-testable.
export function renderAnsi(ansi, name = 'capture') {
  const rawLines = ansi.replace(/\n$/, '').split('\n');
  const lines = rawLines.map(parseLine);
  const maxWidth = Math.max(0, ...lines.map((l) => l.length));

  const width = maxWidth * CELL_W;
  const height = lines.length * CELL_H;

  const body = [];
  for (let y = 0; y < lines.length; y++) {
    for (let x = 0; x < lines[y].length; x++) {
      const cell = lines[y][x];
      if (cell.ch === ' ' && !cell.bg) continue;
      body.push(renderCell(cell, x * CELL_W, y));
    }
  }

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${width} ${height}" width="${width}" height="${height}" role="img" aria-label="Signet TUI capture: ${esc(name)}">\n  <rect width="${width}" height="${height}" fill="${DEFAULT_BG}"/>\n  ${body.join('\n  ')}\n</svg>\n`;
}

function convert(name) {
  const ansi = readFileSync(join(shotsDir, name + '.ansi'), 'utf8');
  const svg = renderAnsi(ansi, name);
  writeFileSync(join(shotsDir, name + '.svg'), svg);
  const m = svg.match(/viewBox="0 0 (\d+) (\d+)"/);
  console.log(`wrote ${join(shotsDir, name + '.svg')} (${m[1]}x${m[2]})`);
}

function main() {
  const files = readdirSync(shotsDir).filter((f) => f.endsWith('.ansi'));
  if (files.length === 0) {
    console.error(`no .ansi captures in ${shotsDir}; run tools/shot first`);
    process.exit(1);
  }
  mkdirSync(shotsDir, { recursive: true });
  for (const f of files.sort()) {
    convert(basename(f, '.ansi'));
  }
}

const isMain =
  process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isMain) main();