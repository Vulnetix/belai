package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

// Address prefixes. A hit row's path begins with one of these, which no
// checkout file name does, so a knowledge row is never mistaken for a file.
const (
	// ProjectScope names the project index in addresses.
	ProjectScope = "project"
	// SessionScope names the session's `@` files in addresses.
	SessionScope = "session"
	addrPrefix   = "kb+"
)

// MaxProfilePaths is the most paths a profile may list.
const MaxProfilePaths = 32

// maxArtifactBytes bounds how much of one scanner artifact is parsed.
const maxArtifactBytes = 64 << 20

// Address builds a document address from a scope name (a profile name,
// ProjectScope or SessionScope) and a path inside it.
func Address(scope, rel string) string {
	return addrPrefix + slug(scope) + "/" + cleanRel(rel)
}

// IsAddress reports whether s is a knowledge address.
func IsAddress(s string) bool { return strings.HasPrefix(s, addrPrefix) }

func slug(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "x"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// cleanRel makes a relative path safe to print in an address: forward slashes,
// no dot segments, only plain characters.
func cleanRel(rel string) string {
	rel = filepath.ToSlash(rel)
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." || p == ".." {
			continue
		}
		var b strings.Builder
		for _, r := range p {
			if r < 0x20 || r == 0x7f || r == ':' || r == '\\' {
				b.WriteByte('_')
				continue
			}
			b.WriteRune(r)
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "/")
}

// Stats is what a sync did, as counts only.
type Stats struct {
	Docs, Chunks, Tokens int
	Dropped              int
	Reused               int
	Skipped              int
	// Truncated is true when the cap stopped ingestion.
	Truncated bool
	// Failed counts documents the Gate could not decide (an error).
	Failed int
}

func (s *Stats) add(r Result) {
	s.Docs++
	s.Chunks += r.Admitted
	s.Dropped += r.Dropped
	if r.Reused {
		s.Reused++
	}
	if r.Truncated {
		s.Truncated = true
	}
}

// Current reports whether the index already holds address with this SHA-256,
// so a caller can skip reading and chunking it.
func (ix *Index) Current(address, sha string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	i := ix.docIndex(address)
	return i >= 0 && sha != "" && ix.docs[i].SHA == sha
}

// Unchanged reports whether the index holds address from a source with this
// size and modification time, so a refresh can skip reading it at all. It is
// only a shortcut: a document whose stat differs is read and its SHA-256 is
// the authority.
func (ix *Index) Unchanged(address string, size int64, mod time.Time) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	i := ix.docIndex(address)
	return i >= 0 && ix.docs[i].Size == size && ix.docs[i].ModNano == mod.UnixNano()
}

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// readText reads a regular, non-symlink file of at most max bytes and returns
// its bytes when they look like text.
func readText(p string, max int64) ([]byte, os.FileInfo, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, info, errors.New("not a regular file")
	}
	if info.Size() > max {
		return nil, info, errors.New("too large")
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, info, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, info, err
	}
	if int64(len(data)) > max {
		return nil, info, errors.New("too large")
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	if !locate.IsText(head) {
		return nil, info, errors.New("binary")
	}
	return data, info, nil
}

// SyncProfile brings ix in line with the documents a profile lists. Each path
// is a file or a directory under the user's own say-so (it comes from the
// profile file, never from a model or a repository). Eligibility is the
// locate inventory's: no symlinks, hidden, binary, oversized, dependency or
// credential-bearing files. Chunks go through gate. The index is trimmed to
// capTokens.
func SyncProfile(ctx context.Context, ix *Index, scope string, paths []string, gate Gate, capTokens int) (Stats, error) {
	var st Stats
	keep := map[string]bool{}
	home, _ := os.UserHomeDir()
	labels := map[string]int{}
	for _, raw := range paths {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		p := raw
		if strings.HasPrefix(p, "~/") && home != "" {
			p = filepath.Join(home, p[2:])
		}
		p, err := filepath.Abs(p)
		if err != nil {
			st.Skipped++
			continue
		}
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			st.Skipped++
			continue
		}
		label := filepath.Base(p)
		labels[label]++
		if labels[label] > 1 {
			label = fmt.Sprintf("%s-%d", label, labels[label])
		}
		type file struct{ abs, rel string }
		var files []file
		if info.IsDir() {
			inv, err := locate.Build(ctx, p)
			if err != nil {
				if ctx.Err() != nil {
					return st, ctx.Err()
				}
				st.Skipped++
				continue
			}
			for _, f := range inv.Files {
				files = append(files, file{filepath.Join(inv.Root, filepath.FromSlash(f.Path)), path.Join(label, f.Path)})
			}
		} else {
			if _, ok := locate.EligibleFile(p, info.Size()); !ok {
				st.Skipped++
				continue
			}
			files = append(files, file{p, label})
		}
		for _, f := range files {
			stop, err := syncFile(ctx, ix, &st, keep, Address(scope, f.rel), f.abs, gate, capTokens)
			if err != nil {
				return st, err
			}
			if stop {
				break
			}
		}
		if st.Truncated {
			break
		}
	}
	ix.Retain(keep)
	ix.Fit(capTokens)
	return st, nil
}

// syncFile ingests one plain-text file. stop is true once the cap is reached.
func syncFile(ctx context.Context, ix *Index, st *Stats, keep map[string]bool, addr, abs string, gate Gate, capTokens int) (stop bool, err error) {
	if fi, serr := os.Lstat(abs); serr == nil && fi.Mode().IsRegular() && ix.Unchanged(addr, fi.Size(), fi.ModTime()) {
		keep[addr] = true
		st.Docs++
		st.Reused++
		return false, nil
	}
	data, info, rerr := readText(abs, locate.MaxFileBytes)
	if rerr != nil {
		st.Skipped++
		return false, nil
	}
	sha := hashBytes(data)
	in := Input{Address: addr, Source: abs, Size: info.Size(), ModTime: info.ModTime(), SHA: sha}
	if !ix.Current(addr, sha) {
		in.Chunks = SplitText(string(data))
	}
	return ingest(ctx, ix, st, keep, in, gate, capTokens)
}

// ingest runs one document through Ingest and folds the outcome into st. A
// gate error fails that document alone (it is not indexed), a cap stops the
// walk, and only a cancelled context aborts.
func ingest(ctx context.Context, ix *Index, st *Stats, keep map[string]bool, in Input, gate Gate, capTokens int) (stop bool, err error) {
	res, ierr := ix.Ingest(ctx, in, gate, capTokens)
	switch {
	case ierr == nil:
		keep[in.Address] = true
		st.add(res)
		return res.Truncated, nil
	case errors.Is(ierr, ErrCapReached):
		st.Truncated = true
		st.Skipped++
		return true, nil
	case ctx.Err() != nil:
		return true, ctx.Err()
	default:
		st.Failed++
		return false, nil
	}
}

// vulnetixDir is the scanner output directory under a project root.
const vulnetixDir = ".vulnetix"

// SyncProject brings ix in line with root/.vulnetix. The harness enumerates it
// itself through scanartifacts (which skips symlinks, special files and
// Belai's own state, and marks superseded copies); no path comes from a model.
// SARIF, CycloneDX and OpenVEX become one chunk per record, composed from
// identifier fields only. Memory, capabilities, package-scan and analyze
// reports and unrecognised text files are chunked as text. Native third-party
// output (a secret scanner's report holds the secrets) and tool logs are never
// indexed.
func SyncProject(ctx context.Context, ix *Index, root string, gate Gate, capTokens int) (Stats, error) {
	var st Stats
	keep := map[string]bool{}
	dir := filepath.Join(root, vulnetixDir)
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		ix.Retain(keep)
		return st, nil
	case err != nil:
		return st, err
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return st, fmt.Errorf("knowledge: %s is not a plain directory", dir)
	}
	arts, err := scanartifacts.Enumerate(dir)
	if err != nil {
		return st, err
	}
	for _, a := range arts {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if a.Superseded {
			continue
		}
		addr := Address(ProjectScope, path.Join(vulnetixDir, filepath.ToSlash(a.Rel)))
		var stop bool
		switch a.Kind {
		case scanartifacts.KindSARIF, scanartifacts.KindCycloneDXSBOM, scanartifacts.KindCycloneDXCBOM,
			scanartifacts.KindCycloneDXAIBOM, scanartifacts.KindOpenVEX, scanartifacts.KindOpenVEXRiskAccepted:
			stop, err = syncRecords(ctx, ix, &st, keep, addr, a, gate, capTokens)
		case scanartifacts.KindMemory, scanartifacts.KindCapabilities, scanartifacts.KindPackagesScan,
			scanartifacts.KindAnalyzeReport:
			stop, err = syncFile(ctx, ix, &st, keep, addr, a.Path, gate, capTokens)
		case scanartifacts.KindUnknown:
			switch strings.ToLower(filepath.Ext(a.Rel)) {
			case ".json", ".yaml", ".yml", ".md", ".txt":
				stop, err = syncFile(ctx, ix, &st, keep, addr, a.Path, gate, capTokens)
			}
		}
		if err != nil {
			return st, err
		}
		if stop {
			break
		}
	}
	ix.Retain(keep)
	ix.Fit(capTokens)
	return st, nil
}

// syncRecords ingests a structured artifact, one chunk per record. Its SHA is
// the file's, so an unchanged artifact is not parsed again.
func syncRecords(ctx context.Context, ix *Index, st *Stats, keep map[string]bool, addr string, a scanartifacts.Artifact, gate Gate, capTokens int) (bool, error) {
	info, err := os.Lstat(a.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxArtifactBytes {
		st.Skipped++
		return false, nil
	}
	if ix.Unchanged(addr, info.Size(), info.ModTime()) {
		keep[addr] = true
		st.Docs++
		st.Reused++
		return false, nil
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		st.Skipped++
		return false, nil
	}
	sha := hashBytes(data)
	in := Input{Address: addr, Source: a.Path, Size: info.Size(), ModTime: info.ModTime(), SHA: sha}
	if !ix.Current(addr, sha) {
		recs, err := scanartifacts.Records(ctx, a.Kind, a.Path, maxArtifactBytes)
		if err != nil && len(recs) == 0 {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			st.Skipped++
			return false, nil
		}
		for i, r := range recs {
			in.Chunks = append(in.Chunks, RawChunk{Start: i + 1, End: i + 1, Text: r})
		}
	}
	return ingest(ctx, ix, st, keep, in, gate, capTokens)
}

// AddText indexes text the user supplied directly, such as an `@` file that
// the session already admitted. name labels the address under SessionScope.
func AddText(ctx context.Context, ix *Index, name, source, text string, capTokens int) (Stats, error) {
	var st Stats
	data := []byte(text)
	in := Input{
		Address: Address(SessionScope, name), Source: source, Size: int64(len(data)), ModTime: time.Now(),
		SHA: hashBytes(data), Chunks: SplitText(text),
	}
	_, err := ingest(ctx, ix, &st, map[string]bool{}, in, nil, capTokens)
	return st, err
}
