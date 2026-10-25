# Bot attribution trailers

Last Updated: 2026-10-26

Inside a Pix sandbox, what Belai commits carries `Co-authored-by`, `Signed-off-by: Pix [bot]` and links to the
session and any vulnerability ids. The package is `internal/attribution`; the rules and the other builders are
in the website's `.repo/bot-attribution.md`. `internal/attribution/testdata/attribution-cases.json` is a copy of the
website's shared cases.

## Rules

- The sandbox Worker writes `~/.pix/attribution` (`PIX_ATTRIBUTION_FILE` overrides the path). Without it Belai is
  not in a Pix sandbox and adds nothing, so a developer's own commits are never signed off by a bot.
- The harness runs git with hooks off (`forge.HardenedGit`), so its commits add the trailers in code:
  `fleet.Workspace.Commit` (session from `Workspace.Session`), the TUI auto-commit and the remote-control
  auto-commit. A model's own `git commit` gets them from the sandbox's global `commit-msg` hook.
- `Workspace.ensureAttribution` runs in `PublishBranch`. It rebases the branch's own commits (`Base..HEAD`) to add
  trailers a repository's own `core.hooksPath` kept the hook from adding. A failed rewrite is aborted and the
  branch is published as it was.
- The duplicate-pull-request close comment carries the trailers too.
- The worker contract tells the model that trailers are added for it and are not to be written, edited or removed.
- Not covered: `internal/teleport/changes/push.go` commits as `teleport@vulnetix.invalid`, which is not Pix.
