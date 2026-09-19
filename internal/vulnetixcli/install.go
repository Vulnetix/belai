package vulnetixcli

import (
	"os"
	"path/filepath"
	"strings"
)

// InstallMethod is how the vulnetix binary landed on this machine.
type InstallMethod string

const (
	InstallBrew    InstallMethod = "brew"
	InstallScoop   InstallMethod = "scoop"
	InstallWinget  InstallMethod = "winget"
	InstallGo      InstallMethod = "go"
	InstallDirect  InstallMethod = "direct"
	InstallUnknown InstallMethod = "unknown"
)

// DetectInstall classifies a binary path into an install method. It resolves
// symlinks and inspects the resolved absolute path, because a Homebrew binary
// is usually reached through multiple symlink hops.
func DetectInstall(path string, getenv func(string) string) (InstallMethod, string) {
	if getenv == nil {
		getenv = os.Getenv
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		// A broken symlink means the CLI is physically absent, not unknown.
		return "", ""
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", ""
	}

	slash := filepath.ToSlash(abs)
	lower := strings.ToLower(slash)

	// Package managers.
	switch {
	case containsOneOf(lower, "/Cellar/", "/linuxbrew/", "/homebrew/"):
		return InstallBrew, prefixFromPath(slash, "Cellar")
	case strings.Contains(lower, "/opt/homebrew"):
		return InstallBrew, prefixFromPath(slash, "bin")
	case containsOneOf(lower, "/scoop/apps/"):
		return InstallScoop, prefixFromPath(slash, "apps")
	case containsOneOf(lower, `/winget/packages/`, `/windowsapps/`):
		return InstallWinget, prefixFromPath(slash, "bin")
	}

	// Env-based hints.
	homebrewPrefix := getenv("HOMEBREW_PREFIX")
	if homebrewPrefix != "" && strings.HasPrefix(lower, strings.ToLower(filepath.ToSlash(homebrewPrefix))) {
		return InstallBrew, homebrewPrefix
	}
	scoop := getenv("SCOOP")
	if scoop != "" && strings.HasPrefix(lower, strings.ToLower(filepath.ToSlash(scoop))) {
		return InstallScoop, scoop
	}
	goBin := getenv("GOBIN")
	if goBin == "" {
		goBin = filepath.Join(getenv("GOPATH"), "bin")
	}
	if strings.TrimSpace(goBin) != "" && strings.HasPrefix(lower, strings.ToLower(filepath.ToSlash(goBin))) {
		return InstallGo, goBin
	}
	home := getenv("HOME")
	if home != "" {
		goHomeBin := filepath.ToSlash(filepath.Join(home, "go", "bin"))
		if strings.HasPrefix(lower, strings.ToLower(goHomeBin)) {
			return InstallGo, goHomeBin
		}
	}

	// Direct installs to common user paths.
	directRoots := []string{"~/.local/bin", "/usr/local/bin", "~/bin", "/usr/bin"}
	for _, root := range directRoots {
		expanded := root
		if strings.HasPrefix(root, "~/") && home != "" {
			expanded = strings.Replace(root, "~", home, 1)
		} else if strings.HasPrefix(root, "~/") {
			expanded = strings.Replace(root, "~", getenv("USERPROFILE"), 1)
		}
		rootSlash := filepath.ToSlash(expanded)
		if strings.HasPrefix(lower, strings.ToLower(rootSlash)) {
			return InstallDirect, rootSlash
		}
	}

	return InstallUnknown, ""
}

// containsOneOf reports whether s contains any of the given substrings
// case-insensitively.
func containsOneOf(s string, substrs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range substrs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// prefixFromPath returns the install prefix up to and including the named
// segment, so callers can render a package-manager command accurately.
func prefixFromPath(slashPath, segment string) string {
	idx := strings.Index(strings.ToLower(slashPath), strings.ToLower(segment))
	if idx < 0 {
		return ""
	}
	slash := strings.IndexByte(slashPath[idx:], '/')
	if slash < 0 {
		return slashPath
	}
	// Return everything up to the segment itself (i.e. the parent of the segment).
	before := slashPath[:idx]
	after := slashPath[idx : idx+slash]
	return before + after
}

// UpdateStatus describes whether an update is available.
type UpdateStatus struct {
	Available bool
	Latest    Version
	Current   Version
	URL       string
	Error     string
}
