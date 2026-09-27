package fleet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// A worker's memory is a short file of lessons it wrote after earlier items,
// one per line, oldest first. It is model-written text: it is classified when
// written and again when a later item reads it, it rides only as a user-turn
// attachment, and it is capped so it can never crowd out the work.

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

// ReadMemory returns a profile's lessons, or "".
func ReadMemory(profile string) string {
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

// ClearMemory deletes a profile's lessons.
func ClearMemory(profile string) error {
	path, err := MemoryPath(profile)
	if err != nil {
		return err
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
	for len(lines) > 0 && len(strings.Join(lines, "\n"))+1 > maxBytes {
		lines = lines[1:]
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
