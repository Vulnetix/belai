// Package gittest gives tests a git that sees only the fixture repositories
// they build. Tests can run where git is already pointed somewhere else: a git
// hook exports GIT_DIR and GIT_INDEX_FILE, and a Belai sandbox exports
// GIT_DIR, GIT_WORK_TREE, the object directories and GIT_CONFIG_* for the
// model's own git, all aimed at metadata the test must not touch. A fixture
// built under t.TempDir() that inherits them resolves to that repository
// instead of its own. The developer's global and system config (signing,
// templates, hooks paths) is also kept out.
//
// Only test files import this package.
package gittest

import (
	"os"
	"strings"
	"testing"
)

// redirecting are the variables that make git open a repository, index,
// object store or config other than the one its working directory implies.
var redirecting = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_NAMESPACE":                    true,
	"GIT_PREFIX":                       true,
	"GIT_CONFIG":                       true,
	"GIT_CONFIG_PARAMETERS":            true,
	"GIT_CONFIG_COUNT":                 true,
}

// isolated is what Env and Isolate set in place of the user's config.
var isolated = []string{
	"GIT_CONFIG_GLOBAL=" + os.DevNull,
	"GIT_CONFIG_SYSTEM=" + os.DevNull,
	"GIT_CONFIG_NOSYSTEM=1",
}

// Inherited reports whether the variable named key redirects git and so must
// not reach a fixture's git.
func Inherited(key string) bool {
	key = strings.ToUpper(key)
	return redirecting[key] || strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_")
}

// Env is os.Environ() without the redirecting variables, with global and
// system config off, and with extra appended (an identity, say). Use it as a
// fixture command's cmd.Env.
func Env(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if Inherited(key) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, isolated...)
	return append(out, extra...)
}

// Isolate does what Env does to the test process's own environment for the
// rest of the test, so code under test that runs git with the process
// environment (proc.ScrubbedEnv) also sees the fixture. Everything is put
// back when the test ends. Like t.Setenv, it cannot be used in a parallel
// test.
func Isolate(t testing.TB) {
	t.Helper()
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if Inherited(key) {
			// t.Setenv records the value to restore; the variable must then be
			// absent, not empty, for git.
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	for _, kv := range isolated {
		key, value, _ := strings.Cut(kv, "=")
		t.Setenv(key, value)
	}
}

// IsolateProcess is Isolate for TestMain, where there is no test to restore
// the environment at: it lasts for the whole package run.
func IsolateProcess() {
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if Inherited(key) {
			os.Unsetenv(key)
		}
	}
	for _, kv := range isolated {
		key, value, _ := strings.Cut(kv, "=")
		os.Setenv(key, value)
	}
}
