// Session intelligence demo: the numbers and glyph helpers the site section
// draws with. The server render and the client script both import this file,
// so the first paint and every later state come from the same code.
//
// The glyph rules mirror internal/tui/components (fillBar, LimitBar,
// Sparkline): eighth-block partial cells, a light-shade trough, and a marker
// that only shows while the fill has not reached it.

const EIGHTHS = '▏▎▍▌▋▊▉';
const SPARK = '▁▂▃▄▅▆▇█';

export interface Bar {
  fill: string;
  trough: string;
}

/** A used share drawn over a trough, width cells wide. */
export function bar(frac: number, width: number): Bar {
  const f = Math.max(0, Math.min(1, frac));
  const cells = f * width;
  let full = Math.floor(cells);
  let eighth = Math.round((cells - full) * 8);
  if (eighth > 7) {
    full++;
    eighth = 0;
  }
  let fill = '█'.repeat(full);
  let used = full;
  if (eighth > 0 && full < width) {
    fill += EIGHTHS[eighth - 1];
    used++;
  }
  return { fill, trough: '░'.repeat(width - used) };
}

export interface LimitBarParts {
  fill: string;
  before: string;
  mark: string;
  after: string;
}

/** A limit bar: the fill, then the trough split around a reset marker. The
 *  marker replaces a trough cell only, so it disappears once the fill covers it. */
export function limitBar(used: number, elapsed: number, width: number): LimitBarParts {
  const { fill, trough } = bar(used, width);
  const filled = width - trough.length;
  let mark = Math.floor(Math.max(0, Math.min(0.9999, elapsed)) * width);
  if (elapsed < 0 || mark < filled) {
    return { fill, before: trough, mark: '', after: '' };
  }
  return {
    fill,
    before: '░'.repeat(mark - filled),
    mark: '╹',
    after: '░'.repeat(width - mark - 1),
  };
}

export interface Cell {
  ch: string;
  on: boolean;
}

/** One cell per value, scaled to the largest; an empty value is the floor bar. */
export function sparkline(values: number[]): Cell[] {
  const peak = Math.max(...values);
  return values.map((v) => ({
    ch: SPARK[v <= 0 ? 0 : Math.max(1, Math.round((v / peak) * 7))],
    on: v > 0,
  }));
}

/** Tokens per hour for the last 24 hours, oldest first. */
export const todayHours = [0, 0, 0, 0, 0, 0, 1, 3, 5, 9, 6, 4, 2, 0, 0, 3, 8, 12, 7, 4, 2, 1, 0, 0];

export interface Win {
  label: string;
  tokens: string;
  sessions: number;
  /** Tokens in millions, for the share of 30 days. */
  total: number;
  share: string;
  models: [string, number, string][];
}

export const windows: Win[] = [
  {
    label: 'today',
    tokens: '34.4M',
    sessions: 3,
    total: 34.4,
    share: '3%',
    models: [
      ['anthropic/claude-sonnet-5', 0.78, '26.8M'],
      ['openrouter/deepseek/deepseek-v3', 0.14, '4.8M'],
      ['anthropic/claude-haiku-4-5', 0.08, '2.8M'],
    ],
  },
  {
    label: 'this week',
    tokens: '625.3M',
    sessions: 19,
    total: 625.3,
    share: '52%',
    models: [
      ['anthropic/claude-sonnet-5', 0.66, '412.6M'],
      ['openrouter/deepseek/deepseek-v3', 0.21, '131.0M'],
      ['anthropic/claude-haiku-4-5', 0.1, '61.7M'],
      ['ollama/qwen3:8b', 0.03, '20.0M'],
    ],
  },
  {
    label: 'last 30 days',
    tokens: '1.2B',
    sessions: 22,
    total: 1206.6,
    share: '100%',
    models: [
      ['anthropic/claude-sonnet-5', 0.61, '735.4M'],
      ['openrouter/deepseek/deepseek-v3', 0.24, '290.0M'],
      ['anthropic/claude-haiku-4-5', 0.12, '145.0M'],
      ['ollama/qwen3:8b', 0.03, '36.2M'],
    ],
  },
];

/** This session's tokens by role. The ledger keeps no role, so this list is
 *  the session's own tally. */
export const roles: [string, number, string][] = [
  ['agent', 0.79, '7.6M'],
  ['security', 0.1, '1.0M'],
  ['compaction', 0.06, '0.6M'],
  ['mode', 0.04, '0.4M'],
];

export interface Row {
  label: string;
  /** A sparkline row (today) or a bar row. */
  spark: boolean;
  fill: string;
  trough: string;
  tokens: string;
  detail: string;
}

export type Mode = 'timeline' | 'models' | 'roles';

const pct = (f: number) => `${String(Math.round(f * 100)).padStart(3, ' ')}%`;

/** The list under the tabs for a mode and a selected window. */
export function rows(mode: Mode, win: number): Row[] {
  if (mode === 'models') {
    return windows[win].models.map(([label, share, tokens]) => ({
      label,
      spark: false,
      ...bar(share, 20),
      tokens,
      detail: pct(share),
    }));
  }
  if (mode === 'roles') {
    return roles.map(([label, share, tokens]) => ({
      label,
      spark: false,
      ...bar(share, 20),
      tokens,
      detail: pct(share),
    }));
  }
  const month = windows[2].total;
  return windows.map((w, i) => ({
    label: w.label,
    spark: i === 0,
    ...bar(w.total / month, 24),
    tokens: w.tokens,
    detail: `${w.sessions} ${w.sessions === 1 ? 'session' : 'sessions'}`,
  }));
}

/** The sub-header above the list. */
export function subHeader(mode: Mode, win: number): string {
  const w = windows[win];
  if (mode === 'models') return `models · ${w.label} · ${w.tokens} · ${w.sessions} sessions`;
  if (mode === 'roles') return 'roles · this session · 9.6M';
  return 'timeline · click a row or press ← →';
}

/** Heatmap days: a 24-digit level string per day (0 to 3), or null for a day
 *  known only from an imported transcript. */
export const heat: { day: string; hours: string | null; total: string; note: string }[] = [
  { day: 'mon', hours: '000000000221332012210000', total: '148.2M', note: '' },
  { day: 'tue', hours: '000000001233332012210000', total: '171.0M', note: '' },
  { day: 'wed', hours: null, total: '96.4M', note: 'day total' },
  { day: 'thu', hours: '000000001223333122100000', total: '203.7M', note: '' },
  { day: 'fri', hours: '000000000110000010000000', total: '41.9M', note: '' },
  { day: 'sat', hours: '000000000000000000000000', total: '0', note: '' },
  { day: 'sun', hours: '000000000123210000000000', total: '34.4M', note: 'today' },
];

export const heatShades = [
  { ch: '░', cls: 'text-[#2F4340]' },
  { ch: '▒', cls: 'text-[#4A6560]' },
  { ch: '▓', cls: 'text-tui-teal' },
  { ch: '█', cls: 'text-tui-teal-soft' },
] as const;

/** The cells of one heatmap row. Hours after now (today's row) are dots. */
export function heatCells(hours: string | null, nowHour = -1): { ch: string; cls: string }[] {
  if (hours === null) return Array.from({ length: 24 }, () => ({ ch: '▒', cls: heatShades[1].cls }));
  return hours.split('').map((d, h) =>
    nowHour >= 0 && h > nowHour ? { ch: '·', cls: heatShades[1].cls } : heatShades[Number(d)],
  );
}

const MUTED = 'text-[#8FA39F]';
const TROUGH = 'text-[#4A6560]';

/** One list row as HTML, shared by the server render and the client script.
 *  Only harness data from this file goes in, never user text. */
export function rowHtml(r: Row, selected: boolean, index: number, labelCh: number): string {
  const pic = r.spark
    ? `<span class="inline-flex">${sparkline(todayHours)
        .map((c) => `<span class="${c.on ? 'text-tui-teal' : TROUGH}">${c.ch}</span>`)
        .join('')}</span><span class="${MUTED}">  24h</span>`
    : `<span class="text-tui-teal">${r.fill}</span><span class="${TROUGH}">${r.trough}</span>`;
  return (
    `<button type="button" data-row="${index}" class="flex min-h-9 w-full items-center whitespace-pre rounded-md px-2 py-1 text-left text-[13px] md:text-sm ${selected ? 'bg-tui-teal-soft/10' : 'hover:bg-tui-teal-soft/5'}">` +
    `<span class="w-[22px] text-tui-teal-soft">${selected ? '▸' : ''}</span>` +
    `<span class="${selected ? 'text-tui-teal-soft' : MUTED}" style="width:${labelCh}ch">${r.label}</span>` +
    pic +
    `<span class="flex-1"></span>` +
    `<span class="pl-5 text-tui-cream">${r.tokens}</span>` +
    `<span class="min-w-[88px] pl-3.5 text-right ${MUTED}">${r.detail}</span>` +
    `</button>`
  );
}

/** The width in characters of the label column for a mode. */
export function labelWidth(mode: Mode): number {
  return mode === 'timeline' ? 14 : 34;
}

/** The whole list for a mode and window. The first row is selected in the models
 *  and roles lists, the current window in the timeline. */
export function listHtml(mode: Mode, win: number): string {
  return rows(mode, win)
    .map((r, i) => rowHtml(r, mode === 'timeline' ? i === win : i === 0, i, labelWidth(mode)))
    .join('');
}
