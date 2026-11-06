package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// GlobalDir returns the global Belai state directory.
// It honours $BELAI_HOME when set, otherwise ~/.vulnetix/belai.
func GlobalDir() (string, error) {
	if v := os.Getenv("BELAI_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".vulnetix", "belai"), nil
}

// LegacyGlobalDir returns the legacy global directory (~/.belai).
func LegacyGlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".belai"), nil
}

// GlobalSettingsPath returns <GlobalDir>/settings.json.
func GlobalSettingsPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// GlobalStatePath returns <GlobalDir>/state.json.
func GlobalStatePath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// KanbanPath returns <GlobalDir>/kanban, the global kanban board file.
func KanbanPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kanban"), nil
}

// UserCredentialsPath returns <GlobalDir>/credentials.json.
func UserCredentialsPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// SessionsDir returns <GlobalDir>/sessions.
func SessionsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
}

// ReviewsDir returns <GlobalDir>/reviews, where finished code reviews are kept,
// one file for each repository and head commit (docs/review.md). It holds
// validated findings. Like the rest of the state directory the OS sandbox
// hides it from every command.
func ReviewsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "reviews"), nil
}

// ProjectDir returns the project-local state directory for a working directory.
func ProjectDir(workdir string) string {
	return filepath.Join(workdir, ".vulnetix")
}

// SymbolIndexDir returns <GlobalDir>/symbols, where the symbol index caches
// what it parsed (docs/symbol-map.md). It lives in the user's state directory,
// never in the repository, so the OS sandbox hides it from every command.
func SymbolIndexDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "symbols"), nil
}

// ProjectSymbolIndexPath returns the cache file of one working directory's
// symbol index: <GlobalDir>/symbols/<WorkdirKey(workdir)>.idx. The file is a
// cache of declarations the harness parsed; it is untrusted on load.
func ProjectSymbolIndexPath(workdir string) (string, error) {
	dir, err := SymbolIndexDir()
	if err != nil {
		return "", err
	}
	abs := workdir
	if a, err := filepath.Abs(workdir); err == nil {
		abs = a
	}
	return filepath.Join(dir, WorkdirKey(abs)+".idx"), nil
}

// ProjectBelaiDir returns the project-local Belai directory.
func ProjectBelaiDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "belai")
}

// ProjectCommitsDir returns the directory of per-session commit records the
// commit workflow writes (docs/commit-workflow.md): one JSON file per session
// holding commit ids, counts and cleaned paths, never a message.
func ProjectCommitsDir(workdir string) string {
	return filepath.Join(ProjectBelaiDir(workdir), "commits")
}

// ProjectLintLoopPath returns the file where the per-edit lint loop keeps its
// counts for a working directory (docs/lint.md): run, finding and exit-code
// numbers and a linter name, never a path, a rule name or a message.
func ProjectLintLoopPath(workdir string) string {
	return filepath.Join(ProjectBelaiDir(workdir), "lint_loop.json")
}

// GlobalPromptsDir returns <GlobalDir>/prompts, the directory of named
// prompt files that make up the global prompt library.
func GlobalPromptsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompts"), nil
}

// ProjectPromptsDir returns <workdir>/.vulnetix/prompts, the directory of
// named prompt files that make up the project-local prompt library.
func ProjectPromptsDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "prompts")
}

// ProjectSettingsPath returns <workdir>/.vulnetix/settings.json.
func ProjectSettingsPath(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "settings.json")
}

// ProjectCredentialsPath returns <workdir>/.vulnetix/belai/credentials.json.
func ProjectCredentialsPath(workdir string) string {
	return filepath.Join(ProjectBelaiDir(workdir), "credentials.json")
}

// ProjectPlansDir returns <workdir>/.vulnetix/plans.
func ProjectPlansDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "plans")
}

// ProjectExportsDir returns <workdir>/.vulnetix/exports, the directory that
// holds exported session Markdown. Exports contain raw tool output, so the
// directory is created 0700 and files are written 0600.
func ProjectExportsDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "exports")
}

// ProjectGoalsDir returns <workdir>/.vulnetix/goals.
func ProjectGoalsDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "goals")
}

// GlobalProcessesDir returns <GlobalDir>/processes, the directory of named
// process files that make up the global process library.
func GlobalProcessesDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "processes"), nil
}

// ProjectProcessesDir returns <workdir>/.vulnetix/processes, the directory of
// named process files that make up the project-local process library.
func ProjectProcessesDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "processes")
}

// ProcessLogsDir returns <GlobalDir>/logs, the directory that holds
// supervised-process log files.
func ProcessLogsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// ForgeDir returns <GlobalDir>/forge, where a worker leaves the git bundle of a
// branch it handed to the forge coordinator. The Pix sandbox's coordinator
// reads <requestId>.bundle from exactly $HOME/.vulnetix/belai/forge, so this is
// GlobalDir's default location there; the model's OS sandbox hides it like the
// rest of the state directory.
func ForgeDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "forge"), nil
}

// ScreenshotsDir returns <GlobalDir>/screenshots, where the Screenshot tool
// keeps its newest captures for the user.
func ScreenshotsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "screenshots"), nil
}

// GlobalSkillsDir returns <GlobalDir>/skills.
func GlobalSkillsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skills"), nil
}

// GlobalCommandsDir returns <GlobalDir>/commands, the directory of custom slash
// command files (one <name>.md each).
func GlobalCommandsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "commands"), nil
}

// ProjectCommandsDir returns <workdir>/.vulnetix/belai/commands, the read-only
// project layer of custom slash commands.
func ProjectCommandsDir(workdir string) string {
	return filepath.Join(ProjectBelaiDir(workdir), "commands")
}

// GlobalHooksDir returns <GlobalDir>/hooks.
func GlobalHooksDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hooks"), nil
}

// WorkdirKey derives a filesystem-safe, deterministic directory name from an
// absolute working-directory path: "<basename>-<8 hex chars of sha256>". It
// is the one key derivation every per-project store uses, so the on-disk key
// format can never drift between the session store and the project prefs.
func WorkdirKey(abs string) string {
	clean := filepath.Clean(abs)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "root"
	}
	sum := sha256.Sum256([]byte(clean))
	return base + "-" + hex.EncodeToString(sum[:4])
}

// ProjectPrefsPath returns the per-project user preference file for a working
// directory: <GlobalDir>/projectprefs/<WorkdirKey(workdir)>.json. It is a
// distinct directory from projectregistry's <GlobalDir>/projects.json.
func ProjectPrefsPath(workdir string) (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projectprefs", WorkdirKey(workdir)+".json"), nil
}

// InputHistoryPath returns the per-project composer input history for a
// working directory: <GlobalDir>/inputhistory/<WorkdirKey(workdir)>.json. It
// holds the `!cmd`, `!!cmd` and slash-command lines the user ran, which are
// not model turns and so never reach a session file as user prompts.
func InputHistoryPath(workdir string) (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "inputhistory", WorkdirKey(workdir)+".json"), nil
}

// Migrate is a one-shot migration from ~/.belai to ~/.vulnetix/belai.
// It runs only when the legacy directory exists and the new one does not.
// If os.Rename fails across filesystems, it falls back to a recursive copy
// and leaves a .migrated marker in the legacy directory.
func Migrate() (migrated bool, err error) {
	legacy, err := LegacyGlobalDir()
	if err != nil {
		return false, err
	}
	newDir, err := GlobalDir()
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(legacy); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := os.Stat(newDir); err == nil {
		return false, nil
	}

	if err := os.Rename(legacy, newDir); err == nil {
		return true, nil
	}

	// Fallback: recursive copy.
	if err := os.MkdirAll(newDir, 0o700); err != nil {
		return false, fmt.Errorf("create new global dir: %w", err)
	}
	if err := copyDir(legacy, newDir); err != nil {
		return false, fmt.Errorf("copy legacy dir: %w", err)
	}
	marker := filepath.Join(legacy, ".migrated")
	if err := os.WriteFile(marker, []byte{}, 0o600); err != nil {
		return false, fmt.Errorf("write migration marker: %w", err)
	}
	return true, nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		sf, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sf.Close()
		df, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(df, sf); err != nil {
			df.Close()
			return err
		}
		return df.Close()
	})
}
