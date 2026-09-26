package depwatch

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		path, content string
		typ, eco      string
		lock          bool
	}{
		{"package.json", "", "package.json", "npm", false},
		{"web/package-lock.json", "", "package-lock.json", "npm", true},
		{"go.mod", "", "go.mod", "golang", false},
		{"svc/Cargo.lock", "", "Cargo.lock", "cargo", true},
		{"environment.yaml", "", "environment.yml", "conda", false},
		{"bb.edn", "", "deps.edn", "clojars", false},
		{"App/App.csproj", "", "*.csproj", "nuget", false},
		{"infra/main.tf", "", "*.tf", "terraform", false},
		{"build/prod.Dockerfile", "", "Dockerfile", "docker", false},
		{"Dockerfile.dev", "", "Dockerfile", "docker", false},
		{"requirements-dev.txt", "", "requirements.txt", "pypi", false},
		{"requirements/base.in", "", "requirements.txt", "pypi", false},
		{"deps.txt", "requests==2.32.3\nurllib3>=2\n", "requirements.txt", "pypi", false},
		{"CMakeLists.txt", "CPMAddPackage(\"gh:fmtlib/fmt#10.2.1\")", "CMakeLists.txt", "cpm", false},
		{"package.yaml", "library:\n  source-dirs: src\ndependencies:\n  - base\n", "package.yaml", "hackage", false},
		{"deploy/stack.yml", "services:\n  web:\n    image: nginx:1.25\n", "compose.yaml", "docker", false},
		{"k8s/app.yaml", "apiVersion: apps/v1\nkind: Deployment\n", "kubernetes.yaml", "kubernetes", false},
		{".github/workflows/ci.yml", "on: push\n", "github-actions.yml", "github-actions", false},
		{"action.yaml", "runs:\n  using: node20\n", "github-actions.yml", "github-actions", false},
		{".circleci/config.yml", "version: 2.1\n", "ci-pipeline", "ci", false},
		{"Jenkinsfile", "", "Jenkinsfile", "ci", false},
		{"scripts/setup.sh", "", "shell-script", "shell", false},
		{"bin/bootstrap", "#!/usr/bin/env bash\napt-get install -y curl\n", "shell-script", "shell", false},
		{"justfile", "", "shell-script", "shell", false},
	}
	for _, c := range cases {
		info, ok := Detect(c.path, c.content)
		if !ok || info.Type != c.typ || info.Ecosystem != c.eco || info.Lock != c.lock {
			t.Errorf("Detect(%q) = %+v, %v; want %s/%s lock=%v", c.path, info, ok, c.typ, c.eco, c.lock)
		}
	}
}

func TestDetectRejects(t *testing.T) {
	for _, c := range []struct{ path, content string }{
		{"main.go", "package main"},
		{"README.md", "# hi"},
		{".npmrc", "registry=https://example.com"}, // registry config: detected, not parsed
		{"settings.gradle", ""},
		{"notes.txt", "remember to buy milk\n"},
		{"names.txt", "requests\nflask\n"}, // bare names are only tentative
		{"CMakeLists.txt", "project(x)"},
		{"config/app.yaml", "port: 8080\n"},
		{"bin/tool", "print('hi')\n"},
	} {
		if info, ok := Detect(c.path, c.content); ok {
			t.Errorf("Detect(%q) = %+v, want no manifest", c.path, info)
		}
	}
}

// Every table entry maps to a profile that is not the catch-all unless its
// ecosystem really is a niche one.
func TestEveryEcosystemHasAProfile(t *testing.T) {
	seen := map[string]bool{}
	for _, info := range manifestFiles {
		seen[ProfileFor(info.Ecosystem)] = true
	}
	for _, info := range manifestExtensions {
		seen[ProfileFor(info.Ecosystem)] = true
	}
	for _, p := range Profiles {
		if !seen[p] {
			t.Errorf("profile %s is reachable from no manifest", p)
		}
	}
}

// TestManifestTableMatchesCLI keeps the port in step with the Vulnetix CLI's
// detector when the CLI is checked out next to this repository: every
// exact-name manifest the CLI parses must be detected here with the same type
// and ecosystem, and every one it does not parse must not be.
func TestManifestTableMatchesCLI(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "cli", "internal", "scan", "detector.go"))
	if err != nil {
		t.Skip("Vulnetix CLI checkout not found next to this repository")
	}
	entry := regexp.MustCompile(`"([^"]+)":\s*\{Type:\s*"([^"]+)",\s*Ecosystem:\s*"([^"]+)"`)
	supportedBlock := regexp.MustCompile(`(?s)var SupportedManifestTypes = map\[string\]bool\{(.*?)\n\}`).FindSubmatch(src)
	if supportedBlock == nil {
		t.Fatal("SupportedManifestTypes not found in the CLI detector")
	}
	supported := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([^"]+)":\s*true`).FindAllSubmatch(supportedBlock[1], -1) {
		supported[string(m[1])] = true
	}
	checked := 0
	for _, m := range entry.FindAllSubmatch(src, -1) {
		name, typ, eco := string(m[1]), string(m[2]), string(m[3])
		if len(name) > 0 && name[0] == '.' && filepath.Ext(name) == name {
			continue // an extension entry, checked through a sample name below
		}
		probe := name
		if filepath.Ext(name) == name {
			probe = "x" + name
		}
		info, ok := Detect(probe, "")
		if !supported[typ] {
			if ok && info.Type == typ {
				t.Errorf("%s (type %s) is not parsed by the CLI but is detected here", name, typ)
			}
			continue
		}
		checked++
		if !ok || info.Type != typ || info.Ecosystem != eco {
			t.Errorf("%s: CLI says %s/%s, here %+v (detected %v)", name, typ, eco, info, ok)
		}
	}
	if checked < 100 {
		t.Fatalf("only %d CLI entries compared; the detector format may have changed", checked)
	}
}

// Every ecosystem profile is an embedded built-in that validates (an invalid
// built-in is skipped silently at load), runs once, and is read-only: a
// dependency triage agent must never install or run a package manager, which
// would execute a malicious package's install scripts.
func TestEcosystemProfilesAreReadOnlyBuiltins(t *testing.T) {
	for _, name := range Profiles {
		p, err := agentprofile.Load(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !p.Builtin || p.Mode != agentprofile.ModeSingle {
			t.Errorf("%s: builtin=%v mode=%q", name, p.Builtin, p.Mode)
		}
		for _, tool := range p.Tools {
			if tool != "Read" && tool != "Grep" && tool != "Glob" {
				t.Errorf("%s: tool %q is not read-only", name, tool)
			}
		}
		if len(p.Tools) == 0 {
			t.Errorf("%s: an empty tool list means every tool", name)
		}
	}
}
