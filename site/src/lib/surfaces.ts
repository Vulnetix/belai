// Where a feature is available. Each section and sub-area on the home page may
// carry a short row of these markers (components/ui/Surfaces.astro). The list
// is closed, so a marker is always one of these six and always reads the same.
export const surfaces = {
  tui: 'The interactive terminal session',
  acp: 'An editor connected over ACP',
  cli: 'The command line: headless runs and subcommands',
  'self-hosted': 'On a machine you run yourself',
  'pix sandbox': 'In a Pix Sandbox that Vulnetix runs for you',
  'vulnetix only': 'Needs a Vulnetix account or service',
} as const;

export type Surface = keyof typeof surfaces;

/** The order the markers render in, whatever order a caller lists them. */
export const surfaceOrder = Object.keys(surfaces) as Surface[];
