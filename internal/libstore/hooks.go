package libstore

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/libitem"
)

func init() { register(libitem.Hook, hookStore{}) }

// A hook is kept as a bundle: <hooks dir>/<name>/hooks.json holding the canonical
// document byte for byte, and the script files beside it at the paths the library
// lists. internal/hooks reads the directory (dialect.go), so what the library
// installs is what runs, and the hash of the document and the files
// (libitem.HashBundle) is what drift compares.
//
// An install builds the whole bundle in a dot-prefixed directory beside the target,
// checks it the way the loader will (every command must name a file in the bundle or
// a program the user allowed), and swaps it in. A bundle that could not run on this
// host is refused, never written half-dead. Only the global hooks directory is
// listed or written.

// Bundle file bounds. They match the library's file store (vdb-site belaiFile*).
const (
	MaxBundleFiles     = 32
	MaxBundleFileBytes = 256 << 10
	MaxBundleBytes     = 2 << 20
	maxBundlePathBytes = 200
)

// BundleFile is one file of a hook bundle to install.
type BundleFile struct {
	// Path is relative to the bundle directory, with forward slashes.
	Path string
	Data []byte
	// SHA256, when set, is the hash the library listed; a file that does not match
	// is refused.
	SHA256 string
}

// LocalFile is one file of a bundle the host holds.
type LocalFile struct {
	Path   string
	SHA256 string
	Size   int
}

var bundlePathSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

type hookStore struct{}

func hooksRoot() (string, error) { return config.GlobalHooksDir() }

// allowedPrograms are the programs the user's own settings let a bundle call.
func allowedPrograms() []string {
	s, err := config.LoadGlobal()
	if err != nil {
		return nil
	}
	return s.Hooks.AllowedProgramList()
}

func fileSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// checkBundlePath reports why a bundle file path cannot be kept, or "".
func checkBundlePath(p string) string {
	if p == "" || len(p) > maxBundlePathBytes || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "a path is not a plain relative path"
	}
	if p == libitem.HookDefinitionFile {
		return libitem.HookDefinitionFile + " is the definition and cannot be a bundle file"
	}
	for _, seg := range strings.Split(p, "/") {
		if !bundlePathSegment.MatchString(seg) {
			return "a path has a segment that is not plain (letters, digits, . _ -, not starting with a dot)"
		}
	}
	return ""
}

// readBundleFiles walks a bundle directory: every file beside the definition, in
// path order. It refuses a symbolic link or special file, and any file outside the
// bounds, with the reason.
func readBundleFiles(dir string) ([]LocalFile, string) {
	files, why := ReadBundleDir(dir)
	if why != "" {
		return nil, why
	}
	out := make([]LocalFile, len(files))
	for i, f := range files {
		out[i] = LocalFile{Path: f.Path, SHA256: f.SHA256, Size: len(f.Data)}
	}
	return out, ""
}

// ReadBundleDir reads every file of a bundle directory beside the definition, with
// its bytes and hash, in path order, under the bounds an install holds a bundle to.
// It refuses a symbolic link or special file, and any file outside the bounds, with
// the reason ("" when it read them all).
func ReadBundleDir(dir string) ([]BundleFile, string) {
	var out []BundleFile
	total := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", rel)
		}
		if d.IsDir() {
			return nil
		}
		if rel == libitem.HookDefinitionFile {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		if why := checkBundlePath(rel); why != "" {
			return fmt.Errorf("%s: %s", rel, why)
		}
		data, why := readRegular(p, MaxBundleFileBytes)
		if why != "" {
			return fmt.Errorf("%s: %s", rel, why)
		}
		if len(data) == 0 || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
			return fmt.Errorf("%s is not bounded text", rel)
		}
		if total += len(data); total > MaxBundleBytes {
			return fmt.Errorf("the files are over %d bytes", MaxBundleBytes)
		}
		out = append(out, BundleFile{Path: rel, Data: data, SHA256: fileSum(data)})
		if len(out) > MaxBundleFiles {
			return fmt.Errorf("more than %d files", MaxBundleFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err.Error()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, ""
}

func (hookStore) list() ([]Local, []Skipped, error) {
	root, err := hooksRoot()
	if err != nil {
		return nil, nil, err
	}
	if fi, err := os.Lstat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	} else if !fi.IsDir() {
		return nil, nil, fmt.Errorf("%s is not a directory (a symbolic link is not followed)", root)
	}
	des, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, err
	}
	var out []Local
	var skipped []Skipped
	for _, de := range des {
		name := de.Name()
		if !de.IsDir() || !hooks.ValidBundleName(name) {
			continue
		}
		dir := filepath.Join(root, name)
		if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		data, why := readRegular(filepath.Join(dir, libitem.HookDefinitionFile), hooks.MaxBundleDefinitionBytes)
		if why != "" {
			skipped = append(skipped, Skipped{Kind: libitem.Hook, Name: name, Reason: libitem.HookDefinitionFile + ": " + why})
			continue
		}
		it, err := libitem.Validate(libitem.Hook, data)
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.Hook, Name: name, Reason: err.Error()})
			continue
		}
		if it.Name != name {
			skipped = append(skipped, Skipped{Kind: libitem.Hook, Name: name, Reason: fmt.Sprintf("the directory is named %s but the hook is named %s", name, it.Name)})
			continue
		}
		files, why := readBundleFiles(dir)
		if why != "" {
			skipped = append(skipped, Skipped{Kind: libitem.Hook, Name: name, Reason: why})
			continue
		}
		l := Local{Kind: libitem.Hook, Name: name, Doc: it.Doc, Files: files}
		bf := make([]libitem.BundleFile, len(files))
		for i, f := range files {
			bf[i] = libitem.BundleFile{Path: f.Path, SHA256: f.SHA256}
		}
		l.SHA256 = libitem.HashBundle(it.Doc, bf)
		out = append(out, l)
	}
	return out, skipped, nil
}

// install is an install with no files: a bundle whose commands name only programs
// the user allowed.
func (h hookStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	return h.installBundle(it, nil, o)
}

func (hookStore) installBundle(it libitem.Item, files []BundleFile, o InstallOptions) (Result, error) {
	root, err := hooksRoot()
	if err != nil {
		return Result{}, err
	}
	if err := untrustedGate(it.Doc); err != nil {
		return Result{}, err
	}
	// Check everything before writing anything.
	if len(files) > MaxBundleFiles {
		return Result{}, refusal("a hook bundle carries at most %d files", MaxBundleFiles)
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range files {
		if why := checkBundlePath(f.Path); why != "" {
			return Result{}, refusal("%s", why)
		}
		if seen[f.Path] {
			return Result{}, refusal("a file is listed twice")
		}
		seen[f.Path] = true
		if len(f.Data) == 0 || len(f.Data) > MaxBundleFileBytes || !utf8.Valid(f.Data) || strings.IndexByte(string(f.Data), 0) >= 0 {
			return Result{}, refusal("a file is not bounded text (1 byte to %d KiB, UTF-8, no NUL)", MaxBundleFileBytes>>10)
		}
		if f.SHA256 != "" && fileSum(f.Data) != f.SHA256 {
			return Result{}, refusal("a file does not match the hash the library listed")
		}
		if err := untrustedGate(f.Data); err != nil {
			return Result{}, err
		}
		if total += len(f.Data); total > MaxBundleBytes {
			return Result{}, refusal("the files are over %d bytes", MaxBundleBytes)
		}
	}

	target := filepath.Join(root, it.Name)
	for _, p := range []string{root, target} {
		fi, err := os.Lstat(p)
		switch {
		case os.IsNotExist(err):
		case err != nil:
			return Result{}, err
		case fi.Mode()&os.ModeSymlink != 0:
			return Result{}, refusal("%s is a symbolic link, so nothing was written", p)
		}
	}
	_, statErr := os.Lstat(target)
	replaced := statErr == nil
	if replaced && !o.Overwrite {
		return Result{}, ErrExists
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Result{}, err
	}
	stage, err := os.MkdirTemp(root, "."+it.Name+".new-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stage)
	if err := os.WriteFile(filepath.Join(stage, libitem.HookDefinitionFile), it.Doc, 0o600); err != nil {
		return Result{}, err
	}
	for _, f := range files {
		dst := filepath.Join(stage, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(dst, f.Data, 0o600); err != nil {
			return Result{}, err
		}
	}
	// Check the bundle the way the loader will. A bundle that cannot run here (a
	// program the user did not allow, a script it does not carry) is refused.
	parsed, err := hooks.ParseDefinition(it.Name, stage, it.Doc, allowedPrograms())
	if err != nil {
		return Result{}, refusal("the hook cannot run on this host: %s", err)
	}
	// A script a command runs is executable; everything else stays private.
	for _, h := range parsed.Hooks {
		if h.AllowedProgram || len(h.Argv) == 0 {
			continue
		}
		if err := os.Chmod(filepath.Join(stage, filepath.FromSlash(h.Argv[0])), 0o700); err != nil {
			return Result{}, err
		}
	}
	old := ""
	if replaced {
		// A crash between the two renames leaves a dot-prefixed directory, which the
		// loader skips.
		old = filepath.Join(root, "."+it.Name+".old-"+filepath.Base(stage)[len("."+it.Name+".new-"):])
		if err := os.Rename(target, old); err != nil {
			return Result{}, err
		}
	}
	if err := os.Rename(stage, target); err != nil {
		if old != "" {
			_ = os.Rename(old, target)
		}
		return Result{}, err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return Result{Replaced: replaced, Where: target}, nil
}

// InstallBundle validates raw as an item of the kind and writes it with the files
// it carries. Only a hook carries files; any other kind with files is refused. The
// refusals are Install's.
func InstallBundle(kind libitem.Kind, raw []byte, files []BundleFile, o InstallOptions) (Result, error) {
	a, ok := adapters[kind]
	if !ok || !libitem.Supported(kind) {
		return Result{}, refusal("this Belai does not store %s items", kind)
	}
	bi, ok := a.(bundleInstaller)
	if !ok {
		if len(files) > 0 {
			return Result{}, refusal("a %s carries no files", kind)
		}
		return Install(kind, raw, o)
	}
	it, err := libitem.Validate(kind, raw)
	if err != nil {
		return Result{}, err
	}
	if o.Name != "" && it.Name != o.Name {
		return Result{}, refusal("the document is named %q, not %q", it.Name, o.Name)
	}
	return bi.installBundle(it, files, o)
}

// RemoveBundle deletes the host's hook bundle of that name. A symbolic link is
// refused, never followed.
func RemoveBundle(name string) error {
	if !libitem.ValidName(libitem.Hook, name) {
		return refusal("that is not a hook name")
	}
	root, err := hooksRoot()
	if err != nil {
		return err
	}
	target := filepath.Join(root, name)
	fi, err := os.Lstat(target)
	switch {
	case os.IsNotExist(err):
		return ErrNotFound
	case err != nil:
		return err
	case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
		return refusal("%s is not a bundle directory", target)
	}
	return os.RemoveAll(target)
}
