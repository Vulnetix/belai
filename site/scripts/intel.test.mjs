import test from 'node:test';
import assert from 'node:assert/strict';
import {
  bar,
  limitBar,
  sparkline,
  todayHours,
  windows,
  rows,
  subHeader,
  heatCells,
  heat,
  listHtml,
} from '../src/lib/intel.ts';

// These mirror the Go tests for components.fillBar, LimitBar and Sparkline
// (internal/tui/components), so the site demo cannot drift from the TUI.

const width = (p) => [...(p.fill + p.before + p.mark + p.after)].length;

test('bar fills whole and eighth cells over a trough of constant width', () => {
  for (const [frac, want] of [
    [0, '░░░░░░░░░░'],
    [0.23, '██▎░░░░░░░'],
    [0.5, '█████░░░░░'],
    [1, '██████████'],
    [1.5, '██████████'],
    [-1, '░░░░░░░░░░'],
  ]) {
    const b = bar(frac, 10);
    assert.equal(b.fill + b.trough, want, `bar(${frac})`);
  }
});

test('limitBar draws the marker in the trough and hides it once the fill covers it', () => {
  const teal = limitBar(0.23, 0.55, 10);
  assert.deepEqual(teal, { fill: '██▎', before: '░░', mark: '╹', after: '░░░░' });
  assert.equal(width(teal), 10);

  // The fill has passed the marker: it disappears (the amber case).
  const ahead = limitBar(0.9, 0.3, 10);
  assert.equal(ahead.mark, '');
  assert.equal(width(ahead), 10);

  // No window: no marker. A marker past the end sits in the last cell.
  assert.equal(limitBar(0.2, -1, 10).mark, '');
  const past = limitBar(0.2, 1.5, 10);
  assert.equal(past.mark, '╹');
  assert.equal(past.after, '');
  assert.equal(width(past), 10);

  // The spent bar has no trough left for a marker.
  const spent = limitBar(1, 0.4, 10);
  assert.equal(spent.fill, '██████████');
  assert.equal(spent.mark, '');

  // The demo's own bars are 56 cells.
  assert.equal(width(limitBar(0.23, 0.55, 56)), 56);
  const weekly = limitBar(0.48, 0.417, 56);
  assert.equal(weekly.mark, '', 'the weekly fill covers its marker, so it is amber');
  assert.equal(width(weekly), 56);
});

test('sparkline scales to the largest cell and keeps an empty cell as the floor', () => {
  const cells = sparkline([0, 1, 2, 4, 8]);
  assert.equal(cells.map((c) => c.ch).join(''), '▁▂▃▅█');
  assert.deepEqual(cells.map((c) => c.on), [false, true, true, true, true]);

  const day = sparkline(todayHours);
  assert.equal(day.length, 24);
  assert.equal(day.map((c) => c.ch).join(''), '▁▁▁▁▁▁▂▃▄▆▅▃▂▁▁▃▆█▅▃▂▂▁▁');
});

test('the three windows are consistent: models add up to the window total', () => {
  assert.equal(windows.length, 3);
  for (const w of windows) {
    const share = w.models.reduce((n, m) => n + m[1], 0);
    assert.ok(Math.abs(share - 1) < 0.011, `${w.label} shares sum to ${share}`);
    const tokens = w.models.reduce((n, m) => n + parseFloat(m[2]), 0);
    assert.ok(Math.abs(tokens - w.total) < 0.5, `${w.label} model tokens sum to ${tokens}, want ${w.total}`);
  }
  // Each window contains the one before it.
  assert.ok(windows[0].total < windows[1].total && windows[1].total < windows[2].total);
});

test('rows: the timeline has today as a sparkline and the others as shares of 30 days', () => {
  const t = rows('timeline', 1);
  assert.deepEqual(t.map((r) => r.label), ['today', 'this week', 'last 30 days']);
  assert.deepEqual(t.map((r) => r.spark), [true, false, false]);
  assert.equal(t[2].fill, '█'.repeat(24));
  assert.equal(t[1].fill + t[1].trough, '████████████▌░░░░░░░░░░░');
  assert.equal(t[0].detail, '3 sessions');

  const m = rows('models', 1);
  assert.equal(m.length, 4);
  assert.equal(m[0].detail, ' 66%');
  assert.equal(m[0].fill + m[0].trough, '█████████████▎░░░░░░');

  const r = rows('roles', 2);
  assert.deepEqual(r.map((x) => x.label), ['agent', 'security', 'compaction', 'mode']);
  assert.equal(r[0].tokens, '7.6M');
});

test('subHeader says what the list is', () => {
  assert.equal(subHeader('models', 1), 'models · this week · 625.3M · 19 sessions');
  assert.equal(subHeader('roles', 0), 'roles · this session · 9.6M');
  assert.match(subHeader('timeline', 2), /^timeline/);
});

test('heat rows: a day with no hours is flat and every row is 24 cells', () => {
  for (const d of heat) assert.equal(heatCells(d.hours).length, 24, d.day);
  const wed = heat.find((d) => d.day === 'wed');
  assert.equal(wed.hours, null);
  assert.equal(wed.note, 'day total');
  assert.ok(heatCells(wed.hours).every((c) => c.ch === '▒'));

  // Today: hours after now are dots.
  const sun = heat.find((d) => d.day === 'sun');
  const cells = heatCells(sun.hours, 14);
  assert.ok(cells.slice(0, 15).every((c) => c.ch !== '·'));
  assert.ok(cells.slice(15).every((c) => c.ch === '·'));
});

test('listHtml marks the selected row and emits one button per row', () => {
  const html = listHtml('timeline', 1);
  assert.equal((html.match(/<button /g) || []).length, 3);
  assert.equal((html.match(/▸/g) || []).length, 1);
  assert.match(html, /data-row="0"/);
  // The selected window's row carries the selection, not the first.
  const second = html.split('<button ')[2];
  assert.match(second, /▸/);

  // Models and roles select their first row.
  const models = listHtml('models', 0);
  assert.equal((models.match(/▸/g) || []).length, 1);
  assert.ok(models.split('<button ')[1].includes('▸'));
  assert.match(models, /anthropic\/claude-sonnet-5/);
  assert.match(listHtml('roles', 0), />security</);
});
