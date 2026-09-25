// Package depwatch is the deterministic dependency-manifest hook. Every file a
// session tool changes is matched against the manifest table the Vulnetix CLI
// parses; a match is coalesced until the turn ends, then a fast-tier sentinel
// role decides whether the change added or updated dependencies, the Vulnetix
// CLI checks the manifest, and findings go to a background agent whose profile
// knows that ecosystem's manifest quirks.
//
// Detection is pure: it reads no file, runs no model and is decided by the
// path and the post-change content alone, so the same change always triggers
// the same way.
package depwatch

import (
	"path"
	"regexp"
	"strings"
)

// Info describes one recognised manifest.
type Info struct {
	// Type is the CLI's canonical manifest type ("package.json", "*.csproj",
	// "ci-pipeline", ...).
	Type string
	// Ecosystem is the CLI's ecosystem name ("npm", "pypi", "golang", ...).
	Ecosystem string
	// Lock marks a lockfile: it records resolved versions rather than
	// declaring dependencies.
	Lock bool
}

func m(typ, eco string) Info       { return Info{Type: typ, Ecosystem: eco} }
func lock(typ, eco string) Info    { return Info{Type: typ, Ecosystem: eco, Lock: true} }
func ci() Info                     { return m("ci-pipeline", "ci") }
func shell() Info                  { return m("shell-script", "shell") }
func docker(typ string) Info       { return m(typ, "docker") }
func sameAs(i Info, t string) Info { i.Type = t; return i }

// manifestFiles mirrors ManifestFiles in the Vulnetix CLI
// (internal/scan/detector.go), restricted to the types the CLI can parse
// (SupportedManifestTypes). Registry-config files (.npmrc, settings.gradle)
// are detected by the CLI but not parsed, so they are absent here.
// TestManifestTableMatchesCLI keeps the two in step when ../cli is checked out.
var manifestFiles = map[string]Info{
	// JavaScript / Node.js
	"package-lock.json":   lock("package-lock.json", "npm"),
	"npm-shrinkwrap.json": lock("package-lock.json", "npm"),
	"package.json":        m("package.json", "npm"),
	"yarn.lock":           lock("yarn.lock", "npm"),
	"pnpm-lock.yaml":      lock("pnpm-lock.yaml", "npm"),
	// Python
	"pyproject.toml":   m("pyproject.toml", "pypi"),
	"requirements.txt": m("requirements.txt", "pypi"),
	"requirements.in":  m("requirements.in", "pypi"),
	"setup.py":         m("setup.py", "pypi"),
	"setup.cfg":        m("setup.cfg", "pypi"),
	"Pipfile":          m("Pipfile", "pypi"),
	"Pipfile.lock":     lock("Pipfile.lock", "pypi"),
	"poetry.lock":      lock("poetry.lock", "pypi"),
	"uv.lock":          lock("uv.lock", "pypi"),
	"pylock.toml":      lock("pylock.toml", "pypi"),
	"environment.yml":  m("environment.yml", "conda"),
	"environment.yaml": m("environment.yml", "conda"),
	// Go
	"go.sum": lock("go.sum", "golang"),
	"go.mod": m("go.mod", "golang"),
	// Ruby
	"Gemfile":      m("Gemfile", "rubygems"),
	"Gemfile.lock": lock("Gemfile.lock", "rubygems"),
	// Rust
	"Cargo.toml": m("Cargo.toml", "cargo"),
	"Cargo.lock": lock("Cargo.lock", "cargo"),
	// JVM
	"pom.xml":          m("pom.xml", "maven"),
	"build.gradle":     m("build.gradle", "maven"),
	"gradle.lockfile":  lock("gradle.lockfile", "maven"),
	"build.gradle.kts": m("build.gradle.kts", "maven"),
	"build.sbt":        m("build.sbt", "maven"),
	"build.lock":       lock("build.lock", "maven"),
	"build.sc":         m("build.sc", "maven"),
	"project.clj":      m("project.clj", "clojars"),
	"deps.edn":         m("deps.edn", "clojars"),
	"bb.edn":           m("deps.edn", "clojars"),
	// PHP
	"composer.json": m("composer.json", "composer"),
	"composer.lock": lock("composer.lock", "composer"),
	// .NET
	"packages.lock.json": lock("packages.lock.json", "nuget"),
	"packages.config":    m("packages.config", "nuget"),
	"paket.dependencies": m("paket.dependencies", "nuget"),
	"paket.lock":         lock("paket.lock", "nuget"),
	// Apple
	"Package.swift":     m("Package.swift", "swift"),
	"Package.resolved":  lock("Package.resolved", "swift"),
	"Podfile":           m("Podfile", "cocoapods"),
	"Podfile.lock":      lock("Podfile.lock", "cocoapods"),
	"Cartfile":          m("Cartfile", "carthage"),
	"Cartfile.resolved": lock("Cartfile.resolved", "carthage"),
	// Dart, Elixir, Erlang, Haskell, OCaml
	"pubspec.yaml":         m("pubspec.yaml", "pub"),
	"pubspec.lock":         lock("pubspec.lock", "pub"),
	"mix.exs":              m("mix.exs", "hex"),
	"mix.lock":             lock("mix.lock", "hex"),
	"rebar.config":         m("rebar.config", "erlang"),
	"rebar.lock":           lock("rebar.lock", "erlang"),
	"stack.yaml":           m("stack.yaml", "stack"),
	"cabal.project.freeze": lock("cabal.project.freeze", "cabal"),
	"opam":                 m("opam", "opam"),
	// Native and build systems
	"conanfile.txt":   m("conanfile.txt", "conan"),
	"conanfile.py":    m("conanfile.py", "conan"),
	"conan.lock":      lock("conan.lock", "conan"),
	"vcpkg.json":      m("vcpkg.json", "vcpkg"),
	"CPM.cmake":       m("CPM.cmake", "cpm"),
	"meson.build":     m("meson.build", "meson"),
	"WORKSPACE":       m("WORKSPACE", "bazel"),
	"WORKSPACE.bazel": m("WORKSPACE", "bazel"),
	"MODULE.bazel":    m("MODULE.bazel", "bazel"),
	"BUCK":            m("BUCK", "buck"),
	"BUCK2":           m("BUCK2", "buck"),
	"build.zig.zon":   m("build.zig.zon", "zig"),
	"flake.nix":       m("flake.nix", "nix"),
	"flake.lock":      lock("flake.lock", "nix"),
	// Other languages
	"Project.toml":  m("Project.toml", "julia"),
	"Manifest.toml": lock("Manifest.toml", "julia"),
	"shard.yml":     m("shard.yml", "crystal"),
	"shard.lock":    lock("shard.lock", "crystal"),
	"deno.json":     m("deno.json", "deno"),
	"deno.lock":     lock("deno.lock", "deno"),
	"DESCRIPTION":   m("DESCRIPTION", "cran"),
	"renv.lock":     lock("renv.lock", "cran"),
	// Containers and orchestration
	"Dockerfile":          docker("Dockerfile"),
	"Containerfile":       docker("Dockerfile"),
	"Gockerfile":          docker("Dockerfile"),
	"Pkgfile":             docker("Dockerfile"),
	"compose.yaml":        docker("compose.yaml"),
	"compose.yml":         docker("compose.yaml"),
	"docker-compose.yaml": docker("compose.yaml"),
	"docker-compose.yml":  docker("compose.yaml"),
	"podman-compose.yaml": docker("compose.yaml"),
	"podman-compose.yml":  docker("compose.yaml"),
	"Chart.yaml":          m("Chart.yaml", "helm"),
	"Chart.yml":           m("Chart.yaml", "helm"),
	// CI/CD pipeline-as-code
	".gitlab-ci.yml": ci(), ".gitlab-ci.yaml": ci(), ".travis.yml": ci(), ".travis.yaml": ci(),
	".cirrus.yml": ci(), ".cirrus.yaml": ci(), ".woodpecker.yml": ci(), ".woodpecker.yaml": ci(),
	".drone.yml": ci(), ".drone.yaml": ci(), ".buddy.yml": ci(), ".buddy.yaml": ci(),
	"azure-pipelines.yml": ci(), "azure-pipelines.yaml": ci(),
	"bitbucket-pipelines.yml": ci(), "bitbucket-pipelines.yaml": ci(),
	"buildspec.yml": ci(), "buildspec.yaml": ci(), "cloudbuild.yml": ci(), "cloudbuild.yaml": ci(),
	"codefresh.yml": ci(), "codefresh.yaml": ci(), "appveyor.yml": ci(), "appveyor.yaml": ci(),
	"semaphore.yml": ci(), "semaphore.yaml": ci(), ".semaphore.yml": ci(),
	"wercker.yml": ci(), "wercker.yaml": ci(), ".circleci.yml": ci(), "bitrise.yml": ci(),
	"codemagic.yaml": ci(), "codeship-steps.yml": ci(), "screwdriver.yaml": ci(), "screwdriver.yml": ci(),
	"zuul.yaml": ci(), ".zuul.yaml": ci(), "harness.yaml": ci(), "skaffold.yaml": ci(), "skaffold.yml": ci(),
	"garden.yml": ci(), "render.yaml": ci(), "cloud-init.yaml": ci(), "user-data.yaml": ci(),
	".gitpod.yml": ci(), ".gitpod.yaml": ci(), "devcontainer.json": ci(), ".devcontainer.json": ci(),
	"shippable.yml": ci(), ".prow.yaml": ci(), "heroku.yml": ci(), "vercel.json": ci(),
	"netlify.toml": ci(), ".space.kts": ci(), ".build.yml": ci(), ".build.yaml": ci(),
	"Taskfile.yml": ci(), "Taskfile.yaml": ci(),
	"Earthfile":               sameAs(ci(), "Dockerfile"),
	"Jenkinsfile":             sameAs(ci(), "Jenkinsfile"),
	"Jenkinsfile.groovy":      sameAs(ci(), "Jenkinsfile"),
	"Jenkinsfile.declarative": sameAs(ci(), "Jenkinsfile"),
	// Shell recipes that install packages with no manifest near them
	"Makefile": shell(), "makefile": shell(), "GNUmakefile": shell(),
	"justfile": shell(), ".justfile": shell(), "Justfile": shell(), "Vagrantfile": shell(),
}

// manifestExtensions mirrors ManifestExtensions in the CLI: files whose name
// carries a project-specific prefix.
var manifestExtensions = map[string]Info{
	".csproj":        m("*.csproj", "nuget"),
	".fsproj":        m("*.csproj", "nuget"),
	".vbproj":        m("*.csproj", "nuget"),
	".tf":            m("*.tf", "terraform"),
	".opam":          m("*.opam", "opam"),
	".cabal":         m("*.cabal", "cabal"),
	".dockerfile":    docker("Dockerfile"),
	".containerfile": docker("Dockerfile"),
	".sh":            shell(), ".bash": shell(), ".zsh": shell(), ".ksh": shell(),
	".bats": shell(), ".ash": shell(), ".dash": shell(), ".fish": shell(),
	".command": shell(), ".mk": shell(), ".ps1": shell(), ".psm1": shell(),
	".cmd": shell(), ".bat": shell(),
}

// Detect reports whether relPath is a manifest the Vulnetix CLI parses. It
// follows the CLI's DetectManifest order — exact name, Dockerfile variants,
// extension, requirements naming, then the content-checked kinds — with
// content being the file's post-change text, so no file is read here.
func Detect(relPath, content string) (Info, bool) {
	slash := strings.TrimPrefix(strings.ReplaceAll(relPath, "\\", "/"), "./")
	base := path.Base(slash)
	lowerBase := strings.ToLower(base)

	if info, ok := manifestFiles[base]; ok {
		return info, true
	}
	if strings.Contains(lowerBase, "dockerfile") || strings.Contains(lowerBase, "containerfile") {
		return docker("Dockerfile"), true
	}
	ext := strings.ToLower(path.Ext(base))
	if info, ok := manifestExtensions[ext]; ok {
		return info, true
	}
	if looksLikeRequirementsName(slash) {
		return m("requirements.txt", "pypi"), true
	}
	if base == "CMakeLists.txt" && strings.Contains(content, "CPMAddPackage") {
		return m("CMakeLists.txt", "cpm"), true
	}
	if base == "package.yaml" && looksLikeHpackYAML(content) {
		return m("package.yaml", "hackage"), true
	}
	if strings.HasSuffix(lowerBase, ".yml") || strings.HasSuffix(lowerBase, ".yaml") {
		if looksLikeComposeYAML(content) {
			return docker("compose.yaml"), true
		}
		if looksLikeKubernetesYAML(content) {
			return m("kubernetes.yaml", "kubernetes"), true
		}
		if looksLikeGitHubActionsPath(slash) {
			return m("github-actions.yml", "github-actions"), true
		}
	}
	if looksLikeCIPipelinePath(slash) {
		return ci(), true
	}
	if ext == "" && looksLikeShellScript(content) {
		return shell(), true
	}
	if reqCandidateExts[ext] && requirementsTextConfident(content) {
		return m("requirements.txt", "pypi"), true
	}
	return Info{}, false
}

func looksLikeKubernetesYAML(content string) bool {
	hasAPIVersion, hasKind := false, false
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) != ln {
			continue // only top-level keys count
		}
		switch {
		case strings.HasPrefix(ln, "apiVersion:"):
			hasAPIVersion = true
		case strings.HasPrefix(ln, "kind:"):
			hasKind = true
		}
		if hasAPIVersion && hasKind {
			return true
		}
	}
	return false
}

func looksLikeHpackYAML(content string) bool {
	if len(content) > 256*1024 {
		return false
	}
	hasDeps, hasSection := false, false
	for _, ln := range strings.Split(content, "\n") {
		t := strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(t, "dependencies:"), strings.HasPrefix(t, "build-depends:"):
			hasDeps = true
		case strings.HasPrefix(t, "library:"), strings.HasPrefix(t, "executable:"),
			strings.HasPrefix(t, "executables:"), strings.HasPrefix(t, "internal-libraries:"),
			strings.HasPrefix(t, "spec-version:"), strings.HasPrefix(t, "ghc-options:"),
			strings.HasPrefix(t, "default-extensions:"):
			hasSection = true
		}
		if hasDeps && hasSection {
			return true
		}
	}
	return false
}

func looksLikeComposeYAML(content string) bool {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "services:") {
		return false
	}
	return strings.Contains(lower, "\n    image:") || strings.Contains(lower, "\n  image:") ||
		strings.Contains(lower, "\n    build:") || strings.Contains(lower, "\n  build:")
}

func looksLikeRequirementsName(slash string) bool {
	lower := strings.ToLower(slash)
	base := path.Base(lower)
	ext := path.Ext(base)
	if ext == ".pip" {
		return true
	}
	if ext == ".txt" || ext == ".in" {
		stem := strings.TrimSuffix(base, ext)
		if strings.Contains(stem, "requirements") || strings.Contains(stem, "constraints") {
			return true
		}
		switch path.Base(path.Dir(lower)) {
		case "requirements", "requires":
			return true
		}
	}
	return false
}

// ciPipelineDirs and ciPipelineNameSuffixes mirror the CLI's CI path rules.
var ciPipelineDirs = []string{
	".circleci", ".buildkite", ".semaphore", ".teamcity", ".woodpecker", ".drone",
	".azure", ".azure-pipelines", ".pipelines", ".ci", "ci", ".gitlab/ci", ".gitlab-ci",
	".tekton", "tekton", ".argo", ".argo-workflows", ".concourse", "concourse",
	".harness", "bamboo-specs", ".cirrus", ".codefresh", ".buddy", ".zuul.d",
	".prow", "prow", ".builds", ".dagger", ".jenkins", ".spinnaker", ".screwdriver",
	".bitbucket", ".codeship", ".devcontainer", ".earthly", ".garden", ".skaffold",
	".gocd", ".appveyor", ".bitrise", ".codemagic", ".travis", ".nomad", ".gitpod",
}

var ciPipelineNameSuffixes = []string{
	"/pipeline.yml", "/pipeline.yaml", "/pipelines.yml", "/pipelines.yaml",
	"/ci.yml", "/ci.yaml", "-pipeline.yml", "-pipeline.yaml",
	"-pipelines.yml", "-pipelines.yaml",
	".gitlab-ci.yml", ".gitlab-ci.yaml",
	"/workflow.yml", "/workflow.yaml", "-workflow.yml", "-workflow.yaml",
	"/.build.yml", "/.build.yaml",
}

func looksLikeCIPipelinePath(slash string) bool {
	slash = strings.ToLower(slash)
	ciExt := strings.HasSuffix(slash, ".yml") || strings.HasSuffix(slash, ".yaml") ||
		strings.HasSuffix(slash, ".json") || strings.HasSuffix(slash, "/config") ||
		strings.HasSuffix(slash, ".kts") || strings.HasSuffix(slash, ".groovy")
	if !ciExt {
		return false
	}
	for _, dir := range ciPipelineDirs {
		if strings.Contains(slash, "/"+dir+"/") || strings.HasPrefix(slash, dir+"/") {
			return true
		}
	}
	if strings.HasSuffix(slash, ".json") || strings.HasSuffix(slash, ".kts") || strings.HasSuffix(slash, ".groovy") {
		return false
	}
	for _, suffix := range ciPipelineNameSuffixes {
		if strings.HasSuffix(slash, suffix) {
			return true
		}
	}
	return false
}

func looksLikeGitHubActionsPath(slash string) bool {
	slash = strings.ToLower(slash)
	base := path.Base(slash)
	if base == "action.yml" || base == "action.yaml" {
		return true
	}
	for _, dir := range []string{".github/workflows/", ".github/actions/", ".gitea/workflows/", ".forgejo/workflows/"} {
		if strings.Contains(slash, "/"+dir) || strings.HasPrefix(slash, dir) {
			return true
		}
	}
	return false
}

func looksLikeShellScript(content string) bool {
	if len(content) > 256*1024 {
		return false
	}
	first := content
	if len(first) > 256 {
		first = first[:256]
	}
	first = strings.TrimSpace(first)
	for _, p := range []string{"#!/bin/sh", "#!/usr/bin/env sh", "#!/bin/bash", "#!/usr/bin/env bash", "#!/bin/zsh", "#!/usr/bin/env zsh"} {
		if strings.HasPrefix(first, p) {
			return true
		}
	}
	return false
}

var (
	reqVersionedLine = regexp.MustCompile(`^[A-Za-z0-9._-]+(\[[A-Za-z0-9,._-]+\])?\s*(===|==|>=|<=|~=|!=|>|<)\s*\S`)
	reqURLRefLine    = regexp.MustCompile(`^[A-Za-z0-9._-]+\s*@\s+\S`)
	reqBareNameLine  = regexp.MustCompile(`^[A-Za-z0-9._-]+(\[[A-Za-z0-9,._-]+\])?$`)
)

var reqCandidateExts = map[string]bool{
	"": true, ".txt": true, ".in": true, ".pip": true, ".reqs": true,
	".requirements": true, ".list": true, ".lst": true, ".text": true,
}

// requirementsTextConfident is the CLI's classifyRequirementsText, keeping
// only its confident verdict: a tentative file (bare names only) is confirmed
// by the CLI against installed packages, which a pure detector cannot do.
func requirementsTextConfident(content string) bool {
	if content == "" || len(content) > 256*1024 {
		return false
	}
	if len(content) > 8192 {
		content = content[:8192]
		if i := strings.LastIndex(content, "\n"); i >= 0 {
			content = content[:i]
		}
	}
	confident := false
	for _, ln := range strings.Split(content, "\n") {
		line := strings.TrimSpace(ln)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "-") {
			if hasPipDirective(line) {
				confident = true
				continue
			}
			return false
		}
		if ci := strings.Index(line, " #"); ci >= 0 {
			line = strings.TrimSpace(line[:ci])
		}
		hadHash := strings.Contains(line, "--hash=")
		if hadHash {
			var keep []string
			for _, f := range strings.Fields(line) {
				if !strings.HasPrefix(f, "--hash=") {
					keep = append(keep, f)
				}
			}
			line = strings.Join(keep, " ")
		}
		spec := line
		if i := strings.Index(spec, ";"); i >= 0 {
			spec = strings.TrimSpace(spec[:i])
		}
		switch {
		case reqVersionedLine.MatchString(spec) || reqURLRefLine.MatchString(spec):
			confident = true
		case reqBareNameLine.MatchString(spec):
			confident = confident || hadHash
		default:
			return false
		}
	}
	return confident
}

func hasPipDirective(line string) bool {
	for _, d := range []string{
		"-r", "-c", "-e", "-i", "-f",
		"--requirement", "--constraint", "--editable", "--index-url",
		"--extra-index-url", "--find-links", "--hash", "--no-binary",
		"--only-binary", "--pre", "--no-index", "--trusted-host",
	} {
		if line == d || strings.HasPrefix(line, d+" ") || strings.HasPrefix(line, d+"=") {
			return true
		}
	}
	return false
}
