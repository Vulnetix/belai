import { Fragment, useMemo, useState } from 'react';

/**
 * The lab comparison, scoped strictly to feature presence. Competitor cells
 * use ✓ / ✗ / · where · means "no public, checkable source at time of
 * writing", never a quality judgement. The framing line (rendered by the
 * parent) is explicit that the models are the good part.
 */

type Cell = 'yes' | 'no' | 'unknown';

type Group = 'untrusted content' | 'boundaries' | 'autonomy' | 'tools & context' | 'integrations';

const GROUPS: Group[] = ['untrusted content', 'boundaries', 'autonomy', 'tools & context', 'integrations'];

interface Row {
  group: Group;
  feature: string;
  signet: Cell;
  claude: Cell;
  codex: Cell;
  cursor: Cell;
  gemini: Cell;
}

// Each row has one line: group, feature, then signet / claude / codex / cursor / gemini.
const row = (group: Group, feature: string, signet: Cell, claude: Cell, codex: Cell, cursor: Cell, gemini: Cell): Row => ({
  group,
  feature,
  signet,
  claude,
  codex,
  cursor,
  gemini,
});

const ROWS: Row[] = [
  // untrusted content
  row('untrusted content', 'sealed delimiters (nonce + SHA-256, stripped at egress), the tools briefing included', 'yes', 'no', 'no', 'no', 'no'),
  row('untrusted content', 'security classifier on every arbitrary tool result, including other agents\' transcripts', 'yes', 'no', 'no', 'no', 'no'),
  row('untrusted content', 'three-phase classifier: in-process BERT gates before any LLM call', 'yes', 'no', 'no', 'no', 'no'),
  row('untrusted content', 'classifier models embedded in the binary (no API key, no download)', 'yes', 'no', 'no', 'no', 'no'),
  row('untrusted content', 'classifier on a separate or local model, routed per use case', 'yes', 'no', 'no', 'no', 'no'),
  row('untrusted content', 'probability-scored tool-call gate (allow / deny / inconclusive)', 'yes', 'no', 'no', 'no', 'no'),
  row('untrusted content', 'tool-call mismatch aborts by default', 'yes', 'unknown', 'unknown', 'unknown', 'unknown'),
  row('untrusted content', 'terminal-escape and bidi sanitising of tool output', 'yes', 'no', 'no', 'no', 'no'),

  // boundaries
  row('boundaries', 'first-run directory gated on explicit trust, headless fails closed', 'yes', 'yes', 'unknown', 'unknown', 'unknown'),
  row('boundaries', 'fixed confinement roots; project-proposed dirs activate only when you accept them', 'yes', 'unknown', 'unknown', 'unknown', 'unknown'),
  row('boundaries', 'plan mode does not offer Bash to the model', 'yes', 'no', 'no', 'no', 'no'),
  row('boundaries', 'one guardrails switch reaching every surface: session, !cmd, @file, background agents, CLI', 'yes', 'no', 'no', 'no', 'no'),
  row('boundaries', 'posture gates with provenance, down to the model behind each classifier phase', 'yes', 'no', 'no', 'no', 'no'),
  row('boundaries', 'skills and hooks load only after strict schema validation', 'yes', 'unknown', 'unknown', 'unknown', 'unknown'),

  // autonomy
  row('autonomy', 'goal loop stops when it is blocked, with no fixed turn count', 'yes', 'no', 'no', 'no', 'no'),
  row('autonomy', 'goal contract drafted by the classifier, sealed by the harness', 'yes', 'no', 'no', 'no', 'no'),
  row('autonomy', 'structured persisted plan documents, best plan kept across passes', 'yes', 'yes', 'unknown', 'unknown', 'unknown'),
  row('autonomy', 'plan-mode explore subagents fanned out in parallel', 'yes', 'yes', 'unknown', 'unknown', 'unknown'),
  row('autonomy', 'reusable agent profiles and background agents', 'yes', 'yes', 'unknown', 'yes', 'unknown'),
  row('autonomy', 'supervised process library with an attempt-capped recovery subagent', 'yes', 'no', 'no', 'no', 'no'),
  row('autonomy', 'classified provider retry plus semantic repair of malformed tool calls', 'yes', 'unknown', 'unknown', 'unknown', 'unknown'),

  // tools & context
  row('tools & context', 'native fixed-argv tool catalogue (~25 utilities)', 'yes', 'no', 'no', 'no', 'no'),
  row('tools & context', 'language-server diagnostics sealed into Edit/Write results', 'yes', 'no', 'no', 'no', 'no'),
  row('tools & context', 'language servers never granted workspace/applyEdit', 'yes', 'unknown', 'unknown', 'unknown', 'unknown'),
  row('tools & context', 'harness-computed repo map with no file contents', 'yes', 'unknown', 'unknown', 'unknown', 'unknown'),
  row('tools & context', "path-free search across other agents' sessions and memory", 'yes', 'no', 'no', 'no', 'no'),
  row('tools & context', 'prompt library as files', 'yes', 'no', 'no', 'no', 'no'),

  // integrations
  row('integrations', 'local providers in the box (ollama, llama-server)', 'yes', 'no', 'yes', 'no', 'no'),
  row('integrations', 'security scanners and an AI firewall built in (Vulnetix)', 'yes', 'no', 'no', 'no', 'no'),
  row('integrations', 'credential import', 'yes', 'yes', 'yes', 'yes', 'yes'),
];

const FILTERS = [
  { id: 'all', label: 'all rows' },
  { id: 'leads', label: 'signet alone' },
  { id: 'parity', label: 'shared elsewhere' },
] as const;

type Filter = (typeof FILTERS)[number]['id'];

const sharedElsewhere = (r: Row) =>
  r.claude === 'yes' || r.codex === 'yes' || r.cursor === 'yes' || r.gemini === 'yes';

function cellChar(c: Cell): string {
  return c === 'yes' ? '✓' : c === 'no' ? '✗' : '·';
}

function cellClass(c: Cell): string {
  if (c === 'yes') return 'text-vx-mint-strong';
  if (c === 'no') return 'text-vx-coral';
  return 'text-vx-ink/50';
}

export default function ComparisonTable() {
  const [filter, setFilter] = useState<Filter>('all');

  const groups = useMemo(() => {
    const rows = ROWS.filter((r) =>
      filter === 'leads' ? !sharedElsewhere(r) : filter === 'parity' ? sharedElsewhere(r) : true,
    );
    return GROUPS.map((g) => ({ group: g, rows: rows.filter((r) => r.group === g) })).filter(
      (g) => g.rows.length > 0,
    );
  }, [filter]);

  return (
    <div>
      <div role="group" aria-label="Filter the comparison" className="mono mb-4 flex flex-wrap gap-2 text-sm">
        {FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            onClick={() => setFilter(f.id)}
            aria-pressed={filter === f.id}
            className={`rounded-md border px-3 py-1.5 transition-colors ${
              filter === f.id
                ? 'border-vx-ink bg-vx-ink text-vx-mint'
                : 'border-vx-ink/30 text-vx-ink hover:border-vx-ink'
            }`}
          >
            {f.label}
          </button>
        ))}
      </div>

      <div className="comparison-scroll rounded-lg border border-vx-ink/30">
        <table className="w-full border-collapse text-left text-sm">
          <caption className="sr-only">
            Feature-presence comparison across Signet, Claude Code, Codex, Cursor and Gemini CLI.
          </caption>
          <thead>
            <tr className="mono border-b border-vx-ink bg-vx-bone text-vx-ink">
              <th scope="col" className="px-4 py-3 font-medium">feature</th>
              <th scope="col" className="bg-vx-mint/15 px-4 py-3 font-bold">signet</th>
              <th scope="col" className="px-4 py-3 font-medium">claude code</th>
              <th scope="col" className="px-4 py-3 font-medium">codex</th>
              <th scope="col" className="px-4 py-3 font-medium">cursor</th>
              <th scope="col" className="px-4 py-3 font-medium">gemini cli</th>
            </tr>
          </thead>
          <tbody>
            {groups.map(({ group, rows }) => (
              <Fragment key={group}>
                <tr className="border-t border-vx-ink/20 bg-vx-sand/40">
                  <th
                    scope="colgroup"
                    colSpan={6}
                    className="mono px-4 py-2 text-xs font-medium uppercase tracking-wider text-vx-ink/70"
                  >
                    {group}
                  </th>
                </tr>
                {rows.map((r) => (
                  <tr key={r.feature} className="border-t border-vx-ink/15">
                    <th scope="row" className="px-4 py-3 font-normal text-vx-ink">{r.feature}</th>
                    <td className={`mono bg-vx-mint/10 px-4 py-3 font-bold ${cellClass(r.signet)}`}>{cellChar(r.signet)}</td>
                    <td className={`mono px-4 py-3 ${cellClass(r.claude)}`}>{cellChar(r.claude)}</td>
                    <td className={`mono px-4 py-3 ${cellClass(r.codex)}`}>{cellChar(r.codex)}</td>
                    <td className={`mono px-4 py-3 ${cellClass(r.cursor)}`}>{cellChar(r.cursor)}</td>
                    <td className={`mono px-4 py-3 ${cellClass(r.gemini)}`}>{cellChar(r.gemini)}</td>
                  </tr>
                ))}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
