package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// GlobalDir returns the global Signet state directory.
// It honours $SIGNET_HOME when set, otherwise ~/.vulnetix/signet.
func GlobalDir() (string, error) {
	if v := os.Getenv("SIGNET_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".vulnetix", "signet"), nil
}

// LegacyGlobalDir returns the legacy global directory (~/.signet).
func LegacyGlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".signet"), nil
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

// ProjectDir returns the project-local state directory for a working directory.
func ProjectDir(workdir string) string {
	return filepath.Join(workdir, ".vulnetix")
}

// ProjectSignetDir returns the project-local Signet directory.
func ProjectSignetDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "signet")
}

// GlobalPromptsPath returns <GlobalDir>/prompts.json.
func GlobalPromptsPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "prompts.json"), nil
}

// ProjectPromptsPath returns <workdir>/.vulnetix/prompts.json.
func ProjectPromptsPath(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "prompts.json")
}

// ProjectSettingsPath returns <workdir>/.vulnetix/settings.json.
func ProjectSettingsPath(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "settings.json")
}

// ProjectCredentialsPath returns <workdir>/.vulnetix/signet/credentials.json.
func ProjectCredentialsPath(workdir string) string {
	return filepath.Join(ProjectSignetDir(workdir), "credentials.json")
}

// ProjectPlansDir returns <workdir>/.vulnetix/plans.
func ProjectPlansDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "plans")
}

// ProjectGoalsDir returns <workdir>/.vulnetix/goals.
func ProjectGoalsDir(workdir string) string {
	return filepath.Join(ProjectDir(workdir), "goals")
}

// GlobalSkillsDir returns <GlobalDir>/skills.
func GlobalSkillsDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "skills"), nil
}

// Migrate is a one-shot migration from ~/.signet to ~/.vulnetix/signet.
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
