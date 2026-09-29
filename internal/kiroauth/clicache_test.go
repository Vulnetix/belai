package kiroauth

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// cliDB builds a kiro-cli database under home with the real sqlite3, or skips
// when this machine has none.
func cliDB(t *testing.T, home string, auth map[string]string, profile string) {
	t.Helper()
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("no sqlite3")
	}
	db := CLIDatabasePaths(home)[0]
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	sql := "CREATE TABLE auth_kv (key TEXT PRIMARY KEY, value TEXT);"
	for k, v := range auth {
		sql += "INSERT INTO auth_kv VALUES (" + quote(k) + "," + quote(v) + ");"
	}
	if profile != "" {
		sql += "CREATE TABLE state (key TEXT PRIMARY KEY, value BLOB);"
		sql += "INSERT INTO state VALUES ('api.codewhisperer.profile', CAST(" + quote(profile) + " AS BLOB));"
	}
	if out, err := exec.Command(bin, db, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v %s", err, out)
	}
}

const (
	cliToken = `{"access_token":"a","refresh_token":"r-cli","expires_at":"2030-01-01T00:00:00Z","region":"us-east-1","start_url":"https://d-1234567890.awsapps.com/start","oauth_flow":"DeviceCode","scopes":["codewhisperer:completions"]}`
	cliReg   = `{"client_id":"cid-cli","client_secret":"cs-cli","client_secret_expires_at":"2030-01-01T00:00:00Z","region":"us-east-1","oauth_flow":"DeviceCode"}`
	cliARN   = "arn:aws:codewhisperer:us-east-1:123456789012:profile/ABCDEF"
)

func TestImportKiroCLI(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	if _, err := ImportKiroCLI(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no database: %v", err)
	}
	cliDB(t, home, map[string]string{
		"kirocli:odic:token":               cliToken,
		"kirocli:odic:device-registration": cliReg,
	}, `{"arn":"`+cliARN+`","profile_name":"default"}`)
	l, err := ImportKiroCLI(home)
	if err != nil {
		t.Fatal(err)
	}
	if l.RefreshToken != "r-cli" || l.ClientID != "cid-cli" || l.ClientSecret != "cs-cli" || l.Region != "us-east-1" ||
		l.StartURL == "" || l.ProfileARN != cliARN || l.ClientSecretExpiresAt == 0 {
		t.Fatalf("login = %#v", l)
	}
}

// Without the Kiro IDE's cache, -import falls back to kiro-cli: the machine in
// the report had only kiro-cli signed in and was told Kiro had not signed in.
func TestImportKiroCacheFallsBackToCLI(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	cliDB(t, home, map[string]string{
		"codewhisperer:odic:token":               cliToken,
		"codewhisperer:odic:device-registration": cliReg,
	}, "")
	l, err := ImportKiroCache(home)
	if err != nil || l.RefreshToken != "r-cli" || l.ProfileARN != "" {
		t.Fatalf("login = %#v, %v", l, err)
	}

	// The IDE's own sign-in still comes first.
	dir := filepath.Join(home, ".aws", "sso", "cache")
	writeFile(t, filepath.Join(dir, "kiro-auth-token.json"), `{"refreshToken":"r-ide","clientIdHash":"abc123","authMethod":"IdC","region":"us-east-1"}`)
	writeFile(t, filepath.Join(dir, "abc123.json"), `{"clientId":"cid","clientSecret":"cs"}`)
	if l, err := ImportKiroCache(home); err != nil || l.RefreshToken != "r-ide" {
		t.Fatalf("IDE login = %#v, %v", l, err)
	}
}

func TestImportKiroCLISocial(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	cliDB(t, home, map[string]string{"kirocli:social:token": `{"access_token":"a","refresh_token":"r"}`}, "")
	if _, err := ImportKiroCLI(home); !errors.Is(err, ErrSocialLogin) {
		t.Fatalf("social: %v", err)
	}
}

// A missing sqlite3 names the fix and never echoes the database output.
func TestImportKiroCLIRowsStayOutOfErrors(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	db := CLIDatabasePaths(home)[0]
	writeFile(t, db, "not sqlite")
	orig := sqliteQuery
	t.Cleanup(func() { sqliteQuery = orig })
	sqliteQuery = func(string, string) ([]byte, error) {
		return []byte(`[{"key":"kirocli:odic:token","value":"{\"refresh_token\":\"SECRET\",\"region\":\"bad region\"}"},` +
			`{"key":"kirocli:odic:device-registration","value":"{\"client_id\":\"c\",\"client_secret\":\"SECRET\"}"}]`), nil
	}
	_, err := ImportKiroCLI(home)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestCLIDatabasePathsOrder(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	p := CLIDatabasePaths("/h")
	mac := "/h/Library/Application Support/kiro-cli/data.sqlite3"
	linux := "/h/.local/share/kiro-cli/data.sqlite3"
	want := []string{linux, mac}
	if runtime.GOOS == "darwin" {
		want = []string{mac, linux}
	}
	if len(p) != 2 || p[0] != want[0] || p[1] != want[1] {
		t.Fatalf("paths = %v", p)
	}
}
