package libstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

func init() { register(libitem.Command, commandStore{}) }

// commandStore keeps a custom slash command as <commands dir>/<name>.md, the
// layout internal/commandlib reads. The file holds the canonical document byte
// for byte, so a command installed from the library hashes to what the library
// holds and no side file is needed. Only the global directory is listed or
// written; a project's commands directory is read-only.
type commandStore struct{}

// maxCommandFile bounds a command file read from disk: the library's limit with
// room for the whitespace canonicalisation removes.
const maxCommandFile = 64 << 10

func commandsDir() (string, error) { return config.GlobalCommandsDir() }

func (commandStore) list() ([]Local, []Skipped, error) {
	root, err := commandsDir()
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
	seen := map[string]bool{}
	for _, de := range des {
		stem, ok := strings.CutSuffix(de.Name(), ".md")
		if !ok || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		data, why := readRegular(filepath.Join(root, de.Name()), maxCommandFile)
		if why != "" {
			skipped = append(skipped, Skipped{Kind: libitem.Command, Name: stem, Reason: why})
			continue
		}
		it, err := libitem.Validate(libitem.Command, data)
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.Command, Name: stem, Reason: err.Error()})
			continue
		}
		if it.Name != stem {
			skipped = append(skipped, Skipped{Kind: libitem.Command, Name: stem, Reason: fmt.Sprintf("the file is named %s.md but the command is named %s", stem, it.Name)})
			continue
		}
		if seen[it.Name] {
			continue
		}
		seen[it.Name] = true
		out = append(out, local(libitem.Command, it.Name, it.Doc))
	}
	return out, skipped, nil
}

func (commandStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	if err := untrustedGate(it.Doc); err != nil {
		return Result{}, err
	}
	root, err := commandsDir()
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(root, it.Name+".md")
	// An install never writes through a link, at the directory or at the file.
	replaced := false
	for _, p := range []string{root, path} {
		fi, err := os.Lstat(p)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			return Result{}, err
		case fi.Mode()&os.ModeSymlink != 0:
			return Result{}, refusal("%s is a symbolic link, so nothing was written", p)
		}
		replaced = p == path
	}
	if replaced && !o.Overwrite {
		return Result{}, ErrExists
	}
	if err := config.WriteGlobalFileAtomic(path, it.Doc); err != nil {
		return Result{}, err
	}
	return Result{Replaced: replaced, Where: path}, nil
}
