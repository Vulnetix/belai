package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/repomemory"
	"github.com/vulnetix/belai/internal/sanitize"
)

// A worker's memory is a short file of lessons it wrote after earlier items,
// one per line, oldest first. It is model-written text: it is classified when
// written and again when a later item reads it, it rides only as a user-turn
// attachment, and it is capped so it can never crowd out the work.
//
// Each lesson is also a profile-scoped note in the shared memory store
// (internal/repomemory), which session sync replicates to Vulnetix: a worker
// on a fresh machine (a Pix Sandbox) whose .md is gone reads its lessons back
// from the store, and the Software Factory console edits or deletes them
// there. A note deleted in the console is a tombstone that removes it here.

// Memory limits.
const (
	maxLessonRunes = 240
	maxLessons     = 3
)

// MemoryPath returns the lessons file for a profile.
func MemoryPath(profile string) (string, error) {
	dir, err := config.AgentsDir()
	if err != nil {
		return "", err
	}
	name := strings.Trim(idUnsafe.ReplaceAllString(strings.ToLower(profile), "-"), "-")
	if name == "" {
		return "", errors.New("fleet: invalid profile name")
	}
	return filepath.Join(dir, "memory", name+".md"), nil
}

// memoryRoot is the shared memory store's root; tests point it elsewhere.
var memoryRoot = repomemory.DefaultRoot

// profileStore is the profile's store in the shared memory, or nil.
func profileStore(profile string) *repomemory.Store {
	root, err := memoryRoot()
	if err != nil {
		return nil
	}
	s, err := repomemory.OpenProfile(root, profile)
	if err != nil {
		return nil
	}
	return s
}

// maxStoreLessons bounds the lessons read back from the store.
const maxStoreLessons = 20

// ReadMemory returns a profile's lessons within the default size, or "".
func ReadMemory(profile string) string { return ReadMemoryN(profile, agentprofile.DefaultMemoryBytes) }

// ReadMemoryN returns a profile's lessons, newest kept within maxBytes. The
// shared store is the record once it has the profile (written here, or pulled
// from Vulnetix on a fresh machine); the .md is read only for lessons written
// before the store existed.
func ReadMemoryN(profile string, maxBytes int) string {
	if s := profileStore(profile); s != nil && s.Exists() {
		notes, err := s.All()
		if err != nil || len(notes) == 0 {
			return ""
		}
		if len(notes) > maxStoreLessons {
			notes = notes[len(notes)-maxStoreLessons:]
		}
		var lines []string
		for _, n := range notes {
			day := time.UnixMilli(n.CreatedAt).UTC().Format("2006-01-02")
			lines = append(lines, "- "+day+" "+strings.Join(strings.Fields(n.Text), " "))
		}
		for len(lines) > 0 && maxBytes > 0 && len(strings.Join(lines, "\n"))+1 > maxBytes {
			lines = lines[1:]
		}
		if len(lines) == 0 {
			return ""
		}
		return strings.Join(lines, "\n") + "\n"
	}
	path, err := MemoryPath(profile)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// ClearMemory deletes a profile's lessons: the .md, and every note in the
// shared store, each as a tombstone that replication sends to Vulnetix.
func ClearMemory(profile string) error {
	path, err := MemoryPath(profile)
	if err != nil {
		return err
	}
	if s := profileStore(profile); s != nil {
		if err := s.Clear(); err != nil {
			return err
		}
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ParseLessons turns a reflection reply into at most three one-line lessons:
// list markers stripped, sanitised, each capped.
func ParseLessons(reply string) []string {
	var out []string
	for _, line := range strings.Split(sanitize.Sanitize(reply), "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•0123456789.) "))
		if line == "" || strings.EqualFold(line, "none") {
			continue
		}
		if utf8.RuneCountInString(line) > maxLessonRunes {
			line = string([]rune(line)[:maxLessonRunes-1]) + "…"
		}
		out = append(out, line)
		if len(out) == maxLessons {
			break
		}
	}
	return out
}

// AppendMemory adds lessons to a profile's file, dropping the oldest lines
// to stay within maxBytes.
func AppendMemory(profile string, lessons []string, maxBytes int, now time.Time) error {
	if len(lessons) == 0 {
		return nil
	}
	path, err := MemoryPath(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
	}
	day := now.Format("2006-01-02")
	for _, l := range lessons {
		lines = append(lines, "- "+day+" "+l)
	}
	if s := profileStore(profile); s != nil {
		for _, l := range lessons {
			_, _ = s.At(now).Append(l, nil, repomemory.Stamp{Profile: profile})
		}
	}
	for len(lines) > 0 && len(strings.Join(lines, "\n"))+1 > maxBytes {
		lines = lines[1:]
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
