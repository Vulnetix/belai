package knowledge

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

// KnowledgeCopyDir is where, under a working directory, the harness places
// copies of documents that live outside the project (a path under ~/ or an
// absolute path in a profile's knowledge.paths). The index's address for such a
// document is kb+<profile>/<label>/<path>; the copy is at
// .vulnetix/knowledge/<label>/<path>.
const KnowledgeCopyDir = ".vulnetix/knowledge"

// ProfileFile is one file a profile's knowledge.paths names, after the
// eligibility rules.
type ProfileFile struct {
	// Rel is the path after the address scope: Address(scope, Rel).
	Rel string
	// Abs is the file on this host.
	Abs string
	// Dest is where a copy of the file goes under a working directory, slash
	// separated: the file's own project-relative path when the listed path was
	// relative, otherwise KnowledgeCopyDir/Rel.
	Dest string
	// Relative is true when the listed path was relative to the project.
	Relative bool
	// Artifact is set for a file of the project's .vulnetix scanner output,
	// which is read as scanner output rather than as a document.
	Artifact *scanartifacts.Artifact
}

// The floor under every listed path, whoever wrote the profile. It is kept to
// what is never reference material: credential stores, the kernel's pseudo
// filesystems and the few system files that hold secrets. A library profile may
// list any ordinary directory, including everything under the home directory
// (a user running as root keeps their documents under /root) and under /etc.
var (
	blockedHome = []string{
		".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".password-store",
		".config/gcloud", ".config/gh", ".config/git", ".local/share/keyrings",
	}
	blockedSystem = []string{
		"/proc", "/sys", "/dev",
		"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/sudoers.d", "/etc/ssh", "/etc/ssl/private",
	}
)

// BlockedAbsolute reports whether an absolute path is one a profile may not
// list: the filesystem root and the home directory themselves (too broad to be
// a document set), credential stores, the kernel's pseudo filesystems, the
// system files that hold secrets, and Belai's own state directory.
func BlockedAbsolute(abs string) bool {
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) || abs == filepath.VolumeName(abs)+string(filepath.Separator) {
		return true
	}
	under := func(root string) bool {
		root = filepath.Clean(root)
		return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
	}
	for _, b := range blockedSystem {
		if under(b) {
			return true
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if abs == filepath.Clean(home) {
			return true
		}
		for _, b := range blockedHome {
			if under(filepath.Join(home, filepath.FromSlash(b))) {
				return true
			}
		}
	}
	if g, err := config.GlobalDir(); err == nil && g != "" && under(g) {
		return true
	}
	return false
}

// hasDotGit reports whether any component of a slash path is .git.
func hasDotGit(rel string) bool {
	for _, p := range strings.Split(filepath.ToSlash(rel), "/") {
		if p == ".git" {
			return true
		}
	}
	return false
}

// EnumerateProfile lists the files a profile's paths name, in the order the
// paths are listed. Each path is absolute, under the home directory (~/), or
// relative to root, the project's trusted repository root. Eligibility is the
// locate inventory's: no symlinks, hidden, binary, oversized, dependency or
// credential-bearing files; a relative path may not leave root, even through a
// symlink, and no path may name .git or a blocked location (BlockedAbsolute).
// The relative path .vulnetix lists the project's scanner output, with the
// files the project index reads (see SyncProject), as artefacts. skipped counts
// the paths and files that were left out.
func EnumerateProfile(ctx context.Context, root string, paths []string) (files []ProfileFile, skipped int, err error) {
	home, _ := os.UserHomeDir()
	labels := map[string]int{}
	for _, raw := range paths {
		if err := ctx.Err(); err != nil {
			return files, skipped, err
		}
		p, relative := raw, false
		switch {
		case strings.HasPrefix(p, "~/"):
			if home == "" {
				skipped++
				continue
			}
			p = filepath.Join(home, p[2:])
		case filepath.IsAbs(p):
		default:
			// A relative path is the project's own. Without a root it names
			// nothing.
			if root == "" {
				skipped++
				continue
			}
			p, relative = filepath.Join(root, filepath.FromSlash(p)), true
		}
		p, aerr := filepath.Abs(p)
		if aerr != nil {
			skipped++
			continue
		}
		cleanRaw := path.Clean(filepath.ToSlash(raw))
		if (relative && hasDotGit(cleanRaw)) || (!relative && BlockedAbsolute(p)) {
			skipped++
			continue
		}
		info, lerr := os.Lstat(p)
		if lerr != nil || info.Mode()&os.ModeSymlink != 0 {
			skipped++
			continue
		}
		if relative && !insideRoot(root, p) {
			// A symlink on the way (a linked docs directory) must not lead out
			// of the repository.
			skipped++
			continue
		}
		if relative && cleanRaw == vulnetixDir {
			arts, aerr := scanartifacts.Enumerate(p)
			if aerr != nil {
				skipped++
				continue
			}
			for _, a := range arts {
				if a.Superseded || !wantArtifact(a) {
					continue
				}
				a := a
				rel := path.Join(vulnetixDir, filepath.ToSlash(a.Rel))
				files = append(files, ProfileFile{Rel: rel, Abs: a.Path, Dest: rel, Relative: true, Artifact: &a})
			}
			continue
		}
		label := filepath.Base(p)
		labels[label]++
		if labels[label] > 1 {
			label = fmt.Sprintf("%s-%d", label, labels[label])
		}
		dest := func(rel string) string {
			if relative {
				return path.Join(cleanRaw, strings.TrimPrefix(rel, label+"/"))
			}
			return path.Join(KnowledgeCopyDir, rel)
		}
		if info.IsDir() {
			inv, berr := locate.Build(ctx, p)
			if berr != nil {
				if ctx.Err() != nil {
					return files, skipped, ctx.Err()
				}
				skipped++
				continue
			}
			for _, f := range inv.Files {
				rel := path.Join(label, f.Path)
				files = append(files, ProfileFile{Rel: rel, Abs: filepath.Join(inv.Root, filepath.FromSlash(f.Path)), Dest: dest(rel), Relative: relative})
			}
			continue
		}
		if _, ok := locate.EligibleFile(p, info.Size()); !ok {
			skipped++
			continue
		}
		d := KnowledgeCopyDir + "/" + label
		if relative {
			d = cleanRaw
		}
		files = append(files, ProfileFile{Rel: label, Abs: p, Dest: d, Relative: relative})
	}
	return files, skipped, nil
}

// wantArtifact reports whether the index reads a .vulnetix artefact: the
// structured scanner kinds, the Belai memory-style files and unrecognised text
// files. Native third-party reports (a secret scanner's findings hold the
// secrets) and tool logs are never read.
func wantArtifact(a scanartifacts.Artifact) bool {
	switch a.Kind {
	case scanartifacts.KindSARIF, scanartifacts.KindCycloneDXSBOM, scanartifacts.KindCycloneDXCBOM,
		scanartifacts.KindCycloneDXAIBOM, scanartifacts.KindOpenVEX, scanartifacts.KindOpenVEXRiskAccepted,
		scanartifacts.KindMemory, scanartifacts.KindCapabilities, scanartifacts.KindPackagesScan,
		scanartifacts.KindAnalyzeReport:
		return true
	case scanartifacts.KindUnknown:
		switch strings.ToLower(filepath.Ext(a.Rel)) {
		case ".json", ".yaml", ".yml", ".md", ".txt":
			return true
		}
	}
	return false
}

// structuredArtifact reports whether an artefact is read as one passage per
// record rather than as text.
func structuredArtifact(a scanartifacts.Artifact) bool {
	switch a.Kind {
	case scanartifacts.KindSARIF, scanartifacts.KindCycloneDXSBOM, scanartifacts.KindCycloneDXCBOM,
		scanartifacts.KindCycloneDXAIBOM, scanartifacts.KindOpenVEX, scanartifacts.KindOpenVEXRiskAccepted:
		return true
	}
	return false
}

// ingestFile ingests one enumerated file: an artefact as scanner output, any
// other as a text document.
func ingestFile(ctx context.Context, ix *Index, st *Stats, keep map[string]bool, addr string, f ProfileFile, gate, scannerGate Gate, capTokens int) (bool, error) {
	if f.Artifact != nil {
		if structuredArtifact(*f.Artifact) {
			return syncRecords(ctx, ix, st, keep, addr, *f.Artifact, scannerGate, capTokens)
		}
		return syncFile(ctx, ix, st, keep, addr, f.Abs, docMeta{Artifact: string(f.Artifact.Kind), Tool: f.Artifact.Tool}, scannerGate, capTokens)
	}
	return syncFile(ctx, ix, st, keep, addr, f.Abs, docMeta{}, gate, capTokens)
}
