package agentimport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Reader limits. A definition is a few text files; anything near these is not one.
const (
	maxFiles      = 1000
	maxFileBytes  = 1 << 20
	maxTotalBytes = 8 << 20
	maxDepth      = 6
	// maxExpanded bounds what an archive may decompress to, kept or not, so a
	// compression bomb is cut off while an export carrying a large database file
	// that is skipped still reads.
	maxExpanded = 256 << 20
)

// tree is the files of an import, keyed by slash-separated path relative to its
// root. It is built once, in memory, from a file, a directory or an archive;
// adapters read it and never touch the disk.
type tree struct {
	files map[string][]byte
	// base is the name of the file, directory or archive the tree came from,
	// without its extension.
	base string
	// skipped counts entries left out on purpose: links, special files,
	// credential files and files over the size limit.
	skipped int
	// oversized names the files left out only for their size, so a missing
	// required file can say why.
	oversized []string
}

// missing is the error for a file an adapter needs and the tree does not hold.
func (t *tree) missing(name string) error {
	for _, o := range t.oversized {
		if o == name || strings.HasSuffix(o, "/"+name) {
			return fmt.Errorf("%s is over %d bytes and was not read", name, maxFileBytes)
		}
	}
	return fmt.Errorf("no %s in the source", name)
}

func (t *tree) has(name string) bool { _, ok := t.files[name]; return ok }

func (t *tree) text(name string) (string, bool) {
	b, ok := t.files[name]
	return string(b), ok
}

// names returns the paths in sorted order.
func (t *tree) names() []string {
	out := make([]string, 0, len(t.files))
	for n := range t.files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// secretName reports whether a file name is a credential store by its name. Such
// a file is never read, so it cannot reach a profile, a report or a log.
func secretName(name string) bool {
	n := strings.ToLower(path.Base(name))
	switch n {
	case "auth.json", "credentials", "credentials.json", ".credentials.json", "secrets.json", "secrets.yaml", "secrets.yml",
		".netrc", ".npmrc", ".pypirc", "id_rsa", "id_ed25519", "id_ecdsa", "token", "token.json", "tokens.json":
		return true
	}
	return n == ".env" || strings.HasPrefix(n, ".env.") ||
		strings.HasSuffix(n, ".pem") || strings.HasSuffix(n, ".key") || strings.HasSuffix(n, ".p12") ||
		strings.HasSuffix(n, ".pfx") || strings.HasSuffix(n, ".token") || strings.HasSuffix(n, ".secret") || strings.HasSuffix(n, ".secrets")
}

// load reads path into a tree. A symbolic link is refused, so the file read is
// the one the user named.
func load(p string) (*tree, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("the path is a symbolic link; name the file or directory itself")
	}
	t := &tree{files: map[string][]byte{}}
	switch {
	case fi.IsDir():
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		t.base = filepath.Base(abs)
		return t, t.readDir(p)
	case fi.Mode().IsRegular():
		lower := strings.ToLower(fi.Name())
		if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
			t.base = fi.Name()[:len(fi.Name())-len(filepath.Ext(fi.Name()))]
			if strings.HasSuffix(strings.ToLower(t.base), ".tar") {
				t.base = t.base[:len(t.base)-len(".tar")]
			}
			f, err := os.Open(p)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			return t, t.readArchive(f)
		}
		if secretName(fi.Name()) {
			return nil, errors.New("that file looks like a credential store and is not read")
		}
		if fi.Size() > maxFileBytes {
			return nil, fmt.Errorf("the file is over %d bytes", maxFileBytes)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		t.base = strings.TrimSuffix(fi.Name(), filepath.Ext(fi.Name()))
		t.files[fi.Name()] = data
		return t, nil
	}
	return nil, errors.New("not a regular file or directory")
}

func (t *tree) add(rel string, data []byte, total *int) error {
	if len(t.files) >= maxFiles {
		return fmt.Errorf("more than %d files", maxFiles)
	}
	if len(data) > maxFileBytes {
		t.skipped++
		t.oversized = append(t.oversized, rel)
		return nil
	}
	*total += len(data)
	if *total > maxTotalBytes {
		return fmt.Errorf("more than %d bytes in all", maxTotalBytes)
	}
	t.files[rel] = data
	return nil
}

func (t *tree) readDir(root string) error {
	total := 0
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory or file that cannot be read is left out, not a reason
			// to refuse the rest.
			if d != nil && d.IsDir() {
				t.skipped++
				return filepath.SkipDir
			}
			t.skipped++
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || strings.Count(rel, "/") >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || secretName(rel) {
			t.skipped++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			t.skipped++
			t.oversized = append(t.oversized, rel)
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.skipped++
			return nil
		}
		return t.add(rel, data, &total)
	})
}

// readArchive reads a gzipped tar into memory. Only regular files count; a link,
// a device or a path that leaves the archive is skipped or refused, and nothing
// is written to disk.
func (t *tree) readArchive(r io.Reader) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(&cappedReader{r: zr, left: maxExpanded})
	total := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("unreadable archive: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(filepath.ToSlash(h.Name), "./"))
		if name == "." || name == "" {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsRune(name, 0) {
			return fmt.Errorf("the archive holds a path outside itself: %q", line(h.Name, 60))
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			t.skipped++
			continue
		}
		if secretName(name) || strings.Count(name, "/") > maxDepth {
			t.skipped++
			continue
		}
		if h.Size > maxFileBytes {
			t.skipped++
			t.oversized = append(t.oversized, name)
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, io.LimitReader(tr, maxFileBytes+1)); err != nil {
			return fmt.Errorf("unreadable archive: %w", err)
		}
		if err := t.add(name, buf.Bytes(), &total); err != nil {
			return err
		}
	}
	t.stripSingleRoot()
	return nil
}

// stripSingleRoot removes a directory every file sits under, as an export that
// wraps its files in a folder named after the profile.
func (t *tree) stripSingleRoot() {
	root := ""
	for n := range t.files {
		top, _, ok := strings.Cut(n, "/")
		if !ok {
			return
		}
		if root == "" {
			root = top
		} else if root != top {
			return
		}
	}
	if root == "" {
		return
	}
	next := make(map[string][]byte, len(t.files))
	for n, d := range t.files {
		next[strings.TrimPrefix(n, root+"/")] = d
	}
	t.files = next
	if t.base == "" {
		t.base = root
	}
}

// cappedReader fails a read once more than left bytes have been returned, so a
// decompression bomb ends in an error, never in an exhausted machine.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, fmt.Errorf("the archive expands to more than %d MiB", maxExpanded>>20)
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
