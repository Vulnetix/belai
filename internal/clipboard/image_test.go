package clipboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeReaders puts stand-in clipboard programs on PATH alone, so a real
// wl-paste or xclip can never run.
func fakeReaders(t *testing.T, scripts map[string]string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("uses the Linux reader programs")
	}
	dir := t.TempDir()
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestReadImageFromWayland(t *testing.T) {
	fakeReaders(t, map[string]string{"wl-paste": `printf '\211PNG-bytes'; printf '%s\n' "$@" > "$FAKE_ARGS"`})
	args := filepath.Join(t.TempDir(), "argv")
	t.Setenv("FAKE_ARGS", args)
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	got, err := ReadImage(context.Background())
	if err != nil || !strings.HasPrefix(string(got), "\x89PNG") {
		t.Fatalf("got %q err %v", got, err)
	}
	b, _ := os.ReadFile(args)
	if string(b) != "--no-newline\n--type\nimage/png\n" {
		t.Fatalf("argv = %q", b)
	}
}

func TestReadImageFallsBackToXclip(t *testing.T) {
	fakeReaders(t, map[string]string{
		"wl-paste": `exit 1`,
		"xclip":    `printf 'PNGDATA'`,
	})
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	got, err := ReadImage(context.Background())
	if err != nil || string(got) != "PNGDATA" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestReadImageTextOnClipboardIsNoImage(t *testing.T) {
	fakeReaders(t, map[string]string{"wl-paste": `echo "No suitable type of content copied" >&2; exit 1`})
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("err = %v, want ErrNoImage", err)
	}
}

func TestReadImageEmptyOutputIsNoImage(t *testing.T) {
	fakeReaders(t, map[string]string{"wl-paste": `exit 0`})
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadImageNamesWhatToInstall(t *testing.T) {
	fakeReaders(t, nil)
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	_, err := ReadImage(context.Background())
	if !errors.Is(err, ErrNoImage) || !strings.Contains(err.Error(), "wl-paste") || !strings.Contains(err.Error(), "xclip") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadImageNoDisplay(t *testing.T) {
	fakeReaders(t, map[string]string{"wl-paste": `printf 'x'`})
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("with no display no reader may run: %v", err)
	}
}

func TestReadImageRefusesAnOversizeClipboard(t *testing.T) {
	yes, err := exec.LookPath("yes")
	head, err2 := exec.LookPath("head")
	if err != nil || err2 != nil {
		t.Skip("yes or head not found")
	}
	fakeReaders(t, map[string]string{"wl-paste": yes + " | " + head + " -c 34000000"})
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	_, err = ReadImage(context.Background())
	if err == nil || errors.Is(err, ErrNoImage) || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadImageDoesNotPassCredentialsToTheReader(t *testing.T) {
	fakeReaders(t, map[string]string{"wl-paste": `printf '%s' "$OPENAI_API_KEY$ANTHROPIC_API_KEY"`})
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("ANTHROPIC_API_KEY", "ak-secret")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", "")
	// A reader that printed a credential would return it as "image" bytes.
	if got, err := ReadImage(context.Background()); err == nil && len(got) > 0 {
		t.Fatalf("the reader saw a credential: %q", got)
	}
}
