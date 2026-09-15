// Package commands holds harness commands backed by the Vulnetix CLI.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

// subcommands are the Vulnetix CLI subcommands run by /code-review.
var subcommands = []string{"scan", "malscan", "license", "bom", "package-firewall", "ai-firewall"}

// Report is the result of a code review.
type Report struct {
	Summary  string
	Manifest []string
}

// CodeReview runs the Vulnetix CLI review subcommands for a workdir.
type CodeReview struct {
	CLI     *vulnetixcli.CLI
	Workdir string
}

// Run executes each subcommand, marks its output as trusted internal content
// via the Role Manager boundary, and writes a summary plus a manifest of the
// resulting files under .vulnetix/.
func (r CodeReview) Run() (Report, error) {
	if r.CLI == nil {
		return Report{}, fmt.Errorf("vulnetix CLI not available")
	}
	r.CLI.Dir = r.Workdir

	var blocks []rolemanager.SystemBlock
	for _, sub := range subcommands {
		out, err := r.CLI.Run(sub)
		if err != nil {
			return Report{}, err
		}
		blocks = append(blocks, rolemanager.SystemBlock{
			Source:  rolemanager.SourceTool,
			Content: fmt.Sprintf("--- vulnetix %s ---\n%s", sub, out),
		})
	}

	// Role Manager boundary: only trusted internal content is promoted.
	if err := rolemanager.VerifyTrustedBlocks(blocks); err != nil {
		return Report{}, err
	}
	var b strings.Builder
	for _, blk := range blocks {
		b.WriteString(blk.Content)
		b.WriteString("\n")
	}
	summary := b.String()

	manifest, err := r.collectManifest()
	if err != nil {
		return Report{}, err
	}
	if err := r.writeArtifacts(summary, manifest); err != nil {
		return Report{}, err
	}
	return Report{Summary: summary, Manifest: manifest}, nil
}

// collectManifest lists the files under .vulnetix/ produced by the CLI.
func (r CodeReview) collectManifest() ([]string, error) {
	root := config.ProjectDir(r.Workdir)
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

func (r CodeReview) writeArtifacts(summary string, manifest []string) error {
	dir := config.ProjectDir(r.Workdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "code-review-summary.md"), []byte(summary), 0o600); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "code-review-manifest.json"), data, 0o600)
}
