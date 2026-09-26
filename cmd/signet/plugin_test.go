package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginDir(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "signet-plugin.json"), []byte(`{"name":"demo","prompts":["prompts"]}`), 0o600)
	os.MkdirAll(filepath.Join(src, "prompts"), 0o700)
	os.WriteFile(filepath.Join(src, "prompts", "hello.md"), []byte("say hello"), 0o600)
	return src
}

// Without a terminal and without -yes, install prints the listing and
// installs nothing.
func TestPluginInstallNeedsConfirmation(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	src := pluginDir(t)
	var out, errb bytes.Buffer
	if code := runPluginCLI(context.Background(), []string{"install", src}, strings.NewReader(""), &out, &errb, false); code == 0 {
		t.Fatal("headless install without -yes succeeded")
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("listing not printed: %q", out.String())
	}
	out.Reset()
	runPluginCLI(context.Background(), []string{"list"}, nil, &out, &errb, false)
	if !strings.Contains(out.String(), "no plugins installed") {
		t.Fatalf("list = %q", out.String())
	}
}

func TestPluginInstallConfirmedOnTTY(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	src := pluginDir(t)
	var out, errb bytes.Buffer
	if code := runPluginCLI(context.Background(), []string{"install", src}, strings.NewReader("y\n"), &out, &errb, true); code != 0 {
		t.Fatalf("install failed: %s", errb.String())
	}
	out.Reset()
	runPluginCLI(context.Background(), []string{"list"}, nil, &out, &errb, false)
	if !strings.HasPrefix(out.String(), "demo\t") || !strings.Contains(out.String(), "enabled") {
		t.Fatalf("list = %q", out.String())
	}
	if code := runPluginCLI(context.Background(), []string{"disable", "demo"}, nil, &out, &errb, false); code != 0 {
		t.Fatal(errb.String())
	}
	if code := runPluginCLI(context.Background(), []string{"remove", "demo"}, nil, &out, &errb, false); code != 0 {
		t.Fatal(errb.String())
	}
}

func TestPluginInstallYes(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	var out, errb bytes.Buffer
	if code := runPluginCLI(context.Background(), []string{"install", "-yes", pluginDir(t)}, nil, &out, &errb, false); code != 0 {
		t.Fatalf("install -yes failed: %s", errb.String())
	}
}
