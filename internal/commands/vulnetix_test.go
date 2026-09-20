package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/vulnetixcli"
)

func writeReviewVulnetix(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "vulnetix")
	script := `#!/bin/sh
# consume hardening flags
while [ $# -gt 0 ]; do
	case "$1" in
		--no-banner|--no-progress|--no-analytics|--disable-memory) shift ;;
		--) shift; break ;;
		-*) shift ;;
		*) break ;;
	esac
done
sub="$1"
echo "fake $sub output"
mkdir -p .vulnetix
printf 'result for %s\n' "$sub" > ".vulnetix/$sub.txt"
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake vulnetix: %v", err)
	}
	return dir
}

func TestVulnetixRunsSubcommandsAndWritesArtifacts(t *testing.T) {
	bin := writeReviewVulnetix(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	cli, err := vulnetixcli.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	workdir := t.TempDir()
	rep, err := (Vulnetix{CLI: cli, Workdir: workdir}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(rep.Summary, "vulnetix scan: ok") {
		t.Fatalf("summary = %q", rep.Summary)
	}

	dir := config.ProjectSignetDir(workdir)
	if _, err := os.Stat(filepath.Join(dir, "code-review-summary.md")); err != nil {
		t.Fatalf("summary file missing: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "code-review-manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest []string
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("manifest json: %v", err)
	}
	want := []string{"ai-firewall.txt", "bom.txt", "license.txt", "malscan.txt", "package-firewall.txt", "scan.txt"}
	if !reflect.DeepEqual(manifest, want) {
		t.Fatalf("manifest = %v, want %v", manifest, want)
	}
}

func TestVulnetixRequiresCLI(t *testing.T) {
	if _, err := (Vulnetix{CLI: nil, Workdir: t.TempDir()}).Run(context.Background()); err == nil {
		t.Fatalf("expected error without CLI")
	}
}

func TestVulnetixRejectsUnknownSubcommand(t *testing.T) {
	bin := writeReviewVulnetix(t)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cli, err := vulnetixcli.Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	_, err = (Vulnetix{CLI: cli, Workdir: t.TempDir(), Subcommands: []string{"scan", "pwn"}}).Run(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}

func TestStatusText(t *testing.T) {
	r := Vulnetix{}
	cap := vulnetixcli.Capabilities{Present: true, Version: vulnetixcli.Version{Major: 3, Minor: 107, Patch: 2}, Install: vulnetixcli.InstallBrew, InstallPrefix: "/homebrew", Auth: vulnetixcli.AuthState{Authenticated: false, Plan: vulnetixcli.PlanCommunity}}
	txt := r.StatusText(cap)
	if !strings.Contains(txt, "vulnetix CLI: v3.107.2") {
		t.Fatalf("status text = %q", txt)
	}
}
