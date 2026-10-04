package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// uuidShape is the shape of a session id Belai mints (a version 4 UUID).
var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Import writes entries as a new session id under key. The file must not exist
// (O_EXCL, like ForkAcross), so an import never replaces a session, and a
// failed write removes what it wrote. The caller built the entries: Import
// writes them as given and checks nothing about their content.
func (s *Store) Import(k Key, id string, entries []Entry) error {
	if !uuidShape.MatchString(id) {
		return fmt.Errorf("import session: %q is not a session id", id)
	}
	path := s.sessionPathForKey(k, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create imported session: %w", err)
	}
	w := bufio.NewWriter(f)
	err = func() error {
		for _, e := range entries {
			data, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("marshal entry: %w", err)
			}
			if _, err := w.Write(append(data, '\n')); err != nil {
				return fmt.Errorf("write imported entry: %w", err)
			}
		}
		return w.Flush()
	}()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

// Remove deletes a session file this process wrote and no longer wants, such as
// an import whose later step failed. A missing file is not an error.
func (s *Store) Remove(k Key, id string) error {
	if !uuidShape.MatchString(id) {
		return fmt.Errorf("remove session: %q is not a session id", id)
	}
	err := os.Remove(s.sessionPathForKey(k, id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
