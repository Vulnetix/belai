package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/secretshape"
)

// SourceScope names the source-tree index in addresses: a file indexed from the
// repository is kb+src/<path relative to the root>.
const SourceScope = "src"

// LoadSource reads the source index stored in dir. A missing file is an empty
// source index; a file that fails its integrity check is an error and is left
// as it is.
func LoadSource(dir string) (*Index, error) {
	ix, err := Load(dir, SourceScope)
	if err != nil {
		return nil, err
	}
	ix.code = true
	return ix, nil
}

// SyncSource brings ix in line with the repository tree at root, the trusted
// repository root and never a worktree. The harness enumerates the tree itself
// through the locate inventory: no symlink is followed, and a dependency or
// hidden directory, an ignored, binary, oversized or credential-named file is
// never listed. A file the secretshape floor finds a private key or a known
// token in is left out as well. Each remaining file is cut into declaration
// chunks (SplitSource), every chunk is sanitised and offered to gate, and a
// chunk the gate does not admit is never stored. A file whose size and
// modification time are unchanged is not read; one whose SHA-256 is unchanged
// is not gated again. maxFileBytes bounds a file, and capTokens the whole index.
// A gate error leaves that file out of the index and the sync goes on.
func SyncSource(ctx context.Context, ix *Index, root string, gate Gate, capTokens, maxFileBytes int) (Stats, error) {
	var st Stats
	if root == "" {
		return st, errors.New("knowledge: the source index needs a repository root")
	}
	if maxFileBytes <= 0 {
		maxFileBytes = 1 << 20
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return st, err
	}
	if BlockedAbsolute(abs) {
		return st, fmt.Errorf("knowledge: %s is not a repository root the source index may read", abs)
	}
	inv, err := locate.Build(ctx, abs)
	if err != nil {
		return st, err
	}
	keep := map[string]bool{}
	for _, f := range inv.Files {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		if f.Size > int64(maxFileBytes) {
			st.Skipped++
			continue
		}
		addr := Address(SourceScope, f.Path)
		file := filepath.Join(inv.Root, filepath.FromSlash(f.Path))
		if fi, serr := os.Lstat(file); serr == nil && fi.Mode().IsRegular() && ix.Unchanged(addr, fi.Size(), fi.ModTime()) {
			keep[addr] = true
			st.Docs++
			st.Reused++
			continue
		}
		// The walk never follows a link, but a directory swapped for one since
		// is caught here before a byte is read.
		if !insideRoot(inv.Root, file) {
			st.Skipped++
			continue
		}
		data, info, rerr := readText(file, int64(maxFileBytes))
		if rerr != nil || secretshape.Holds(data) {
			st.Skipped++
			continue
		}
		in := Input{Address: addr, Source: file, Size: info.Size(), ModTime: info.ModTime(), SHA: hashBytes(data)}
		if !ix.Current(addr, in.SHA) {
			in.Chunks = SplitSource(f.Path, string(data))
		}
		stop, ierr := ingest(ctx, ix, &st, keep, in, gate, capTokens)
		if ierr != nil {
			return st, ierr
		}
		if stop {
			break
		}
	}
	ix.Retain(keep)
	ix.Fit(capTokens)
	return st, nil
}
