// The home page's sections, in page order. Each entry is [id, short label].
// The side rail, the compact jump menu and the chapter label above every
// section all read this list, so a section's number is its position here and
// cannot drift from the rail.
export const sections = [
  ['intro', 'intro'],
  ['beliefs', 'beliefs'],
  ['trust', 'trust'],
  ['classifier', 'classifier'],
  ['sealed', 'sealed'],
  ['labs', 'labs'],
  ['modes', 'modes'],
  ['tools', 'tools & sandbox'],
  ['diagnostics', 'diagnostics'],
  ['permissions', 'permissions'],
  ['agents', 'agents & crews'],
  ['memory', 'memory & knowledge'],
  ['processes', 'processes & recovery'],
  ['budgets', 'budgets'],
  ['intel', 'session intelligence'],
  ['providers', 'providers'],
  ['routing', 'routing'],
  ['vulnetix', 'vulnetix'],
  ['kanban', 'kanban'],
  ['web', 'web sessions'],
  ['extend', 'extend & integrate'],
  ['cli', 'cli'],
  ['qol', 'qol'],
  ['start', 'getting started'],
  ['faq', 'faq'],
] as const;

export interface Chapter {
  n: number;
  total: number;
  label: string;
}

/** The chapter number and label for a section id, or null for an id that is not a home-page section. */
export function chapterOf(id: string | undefined): Chapter | null {
  if (!id) return null;
  const i = sections.findIndex(([s]) => s === id);
  if (i < 0) return null;
  return { n: i + 1, total: sections.length, label: sections[i][1] };
}

/** "07 / 25" */
export function chapterNumber(c: Pick<Chapter, 'n' | 'total'>): string {
  return `${String(c.n).padStart(2, '0')} / ${c.total}`;
}
