import { useMemo, useState } from 'react';

/**
 * The lab comparison, scoped strictly to feature presence. Signet's column
 * traces to files in this repository (the trace column). Competitor cells use
 * ✓ / ✗ / · where · means "no public, checkable source at time of writing",
 * never a quality judgement. The framing line (rendered by the parent) is
 * explicit that the models are the good part.
 */

type Cell = 'yes' | 'no' | 'unknown';

interface Row {
  feature: string;
  signet: string; // file/test that evidences the claim
  signetCell: Cell;
  claude: Cell;
  codex: Cell;
  cursor: Cell;
  gemini: Cell;
}

const ROWS: Row[] = [
  {
    feature: 'sealed delimiters (nonce + SHA-256, stripped at egress)',
    signet: 'internal/rolemanager · e2e',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'security classifier on every untrusted tool result',
    signet: 'internal/rolemanager',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'classifier on a separate/local model',
    signet: 'internal/rolemanager · local providers',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'posture gates with provenance',
    signet: 'internal/posture',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'plan mode with Bash absent, not restricted',
    signet: 'internal/tui/view.go · e2e',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'structured persisted plan documents',
    signet: 'internal/plans',
    signetCell: 'yes',
    claude: 'yes',
    codex: 'unknown',
    cursor: 'unknown',
    gemini: 'unknown',
  },
  {
    feature: 'goal loop stops when blocked, not after a turn count',
    signet: 'internal/agent/passloop.go',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'native fixed-argv tool catalogue (~25 utilities)',
    signet: 'internal/tools/catalog.go',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'harness-computed repo map',
    signet: 'internal/repoindex',
    signetCell: 'yes',
    claude: 'yes',
    codex: 'unknown',
    cursor: 'unknown',
    gemini: 'unknown',
  },
  {
    feature: 'prompt library as files',
    signet: 'internal/prompt',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'credential import',
    signet: 'internal/credentials',
    signetCell: 'yes',
    claude: 'yes',
    codex: 'yes',
    cursor: 'yes',
    gemini: 'yes',
  },
  {
    feature: 'terminal-escape sanitising',
    signet: 'internal/tools',
    signetCell: 'yes',
    claude: 'no',
    codex: 'no',
    cursor: 'no',
    gemini: 'no',
  },
  {
    feature: 'CI-enforced docs',
    signet: '.github/workflows/ci.yml · docs/',
    signetCell: 'yes',
    claude: 'unknown',
    codex: 'unknown',
    cursor: 'unknown',
    gemini: 'unknown',
  },
];

const FILTERS = [
  { id: 'all', label: 'all rows' },
  { id: 'leads', label: 'signet alone' },
  { id: 'parity', label: 'shared elsewhere' },
] as const;

type Filter = (typeof FILTERS)[number]['id'];

function cellChar(c: Cell): string {
  return c === 'yes' ? '✓' : c === 'no' ? '✗' : '·';
}

function cellClass(c: Cell, signet: boolean): string {
  if (signet) return c === 'yes' ? 'text-vx-mint-strong' : 'text-vx-coral';
  if (c === 'yes') return 'text-vx-mint-strong';
  if (c === 'no') return 'text-vx-coral';
  return 'text-vx-ink opacity-50';
}

export default function ComparisonTable() {
  const [filter, setFilter] = useState<Filter>('all');

  const rows = useMemo(() => {
    switch (filter) {
      case 'leads':
        return ROWS.filter(
          (r) => r.claude !== 'yes' && r.codex !== 'yes' && r.cursor !== 'yes' && r.gemini !== 'yes',
        );
      case 'parity':
        return ROWS.filter(
          (r) => r.claude === 'yes' || r.codex === 'yes' || r.cursor === 'yes' || r.gemini === 'yes',
        );
      default:
        return ROWS;
    }
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

      <div className="comparison-scroll rounded-lg border border-vx-ink">
        <table className="w-full border-collapse text-left text-sm">
          <caption className="sr-only">
            Feature-presence comparison across Signet, Claude Code, Codex, Cursor and Gemini CLI.
          </caption>
          <thead>
            <tr className="mono bg-vx-ink text-vx-cream">
              <th scope="col" className="px-4 py-3 font-medium">feature</th>
              <th scope="col" className="px-4 py-3 font-medium">signet</th>
              <th scope="col" className="px-4 py-3 font-medium">claude code</th>
              <th scope="col" className="px-4 py-3 font-medium">codex</th>
              <th scope="col" className="px-4 py-3 font-medium">cursor</th>
              <th scope="col" className="px-4 py-3 font-medium">gemini cli</th>
              <th scope="col" className="px-4 py-3 font-medium">trace</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.feature} className="border-t border-vx-ink/20">
                <th scope="row" className="px-4 py-3 font-normal">{r.feature}</th>
                <td className={`mono px-4 py-3 font-bold ${cellClass(r.signetCell, true)}`}>{cellChar(r.signetCell)}</td>
                <td className={`mono px-4 py-3 ${cellClass(r.claude, false)}`}>{cellChar(r.claude)}</td>
                <td className={`mono px-4 py-3 ${cellClass(r.codex, false)}`}>{cellChar(r.codex)}</td>
                <td className={`mono px-4 py-3 ${cellClass(r.cursor, false)}`}>{cellChar(r.cursor)}</td>
                <td className={`mono px-4 py-3 ${cellClass(r.gemini, false)}`}>{cellChar(r.gemini)}</td>
                <td className="mono px-4 py-3 text-xs text-vx-ink/60">{r.signet}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}