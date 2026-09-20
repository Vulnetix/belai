import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  parseLine,
  renderCell,
  renderAnsi,
  sgrDeltas,
  DEFAULT_BG,
  DEFAULT_FG,
  CELL_W,
  CELL_H,
} from './ansi-to-svg.mjs';

test('sgrDeltas folds truecolour fg/bg and reset', () => {
  const deltas = [...sgrDeltas('38;2;58;195;179;48;2;28;52;48;1;0')];
  assert.deepEqual(deltas.slice(0, 3), [
    { type: 'fg', value: 'rgb(58,195,179)' },
    { type: 'bg', value: 'rgb(28,52,48)' },
    { type: 'bold', value: true },
  ]);
  assert.deepEqual(deltas.at(-1), { type: 'reset' });
});

test('parseLine assigns fg and bg per character', () => {
  const cells = parseLine('\x1b[38;2;58;195;179m\x1b[48;2;28;52;48mA\x1b[0mB');
  assert.equal(cells[0].ch, 'A');
  assert.equal(cells[0].fg, 'rgb(58,195,179)');
  assert.equal(cells[0].bg, 'rgb(28,52,48)');
  assert.equal(cells[1].ch, 'B');
  assert.equal(cells[1].fg, null);
  assert.equal(cells[1].bg, null);
});

test('parseLine applies reverse video by swapping fg and bg', () => {
  const cells = parseLine('\x1b[38;2;1;2;3m\x1b[48;2;4;5;6m\x1b[7mX');
  assert.equal(cells[0].fg, 'rgb(4,5,6)');
  assert.equal(cells[0].bg, 'rgb(1,2,3)');
});

test('half-block renders two rects and empty halves fall back to ink', () => {
  // fg set, bg unset -> bottom half must be ink, not the text colour.
  const out = renderCell({ ch: '\u2580', fg: 'rgb(58,195,179)', bg: null, bold: false }, 0, 0);
  assert.equal(
    out,
    `<rect x="0" y="0" width="${CELL_W}" height="${CELL_H / 2}" fill="rgb(58,195,179)"/>` +
      `<rect x="0" y="${CELL_H / 2}" width="${CELL_W}" height="${CELL_H / 2}" fill="${DEFAULT_BG}"/>`,
  );
});

test('text cells escape XML and honour bold', () => {
  const out = renderCell({ ch: '<', fg: 'rgb(58,195,179)', bg: null, bold: true }, 0, 0);
  assert.ok(out.includes('&lt;'), 'angle bracket must be escaped');
  assert.ok(out.includes('font-weight="700"'), 'bold must set weight 700');
});

test('text cells fall back to the default foreground', () => {
  const out = renderCell({ ch: 'A', fg: null, bg: null, bold: false }, 0, 0);
  assert.ok(out.includes(`fill="${DEFAULT_FG}"`), 'unset fg must use the default text colour');
});

test('renderAnsi sizes the viewBox to the widest line and line count', () => {
  const svg = renderAnsi('\x1b[38;2;58;195;179mAB\x1b[0m\nCDE', 'probe');
  assert.match(svg, /viewBox="0 0 30 40"/, '3 cells wide, 2 lines tall');
  assert.ok(svg.includes('role="img"'));
  assert.ok(svg.includes(`fill="${DEFAULT_BG}"`));
});

test('renderAnsi escapes the capture name in the aria-label', () => {
  const svg = renderAnsi('A', 'a<b>&c');
  assert.ok(svg.includes('a&lt;b&gt;&amp;c'));
});