// Command voiceprep fetches the speech model that release builds embed. It
// downloads ggml-tiny.en-q5_1.bin from Hugging Face (checked against the SHA-256
// Hugging Face reports and the one pinned in internal/voice) and copies it to
// internal/voice/assets, where the belai_voice build tag embeds it. The
// directory is gitignored, so the weights never enter the repository.
//
// Run it from anywhere inside the repository:
//
//	go run ./tools/voiceprep
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/voice"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "voiceprep:", err)
		os.Exit(1)
	}
	out := filepath.Join(root, "internal", "voice", "assets")
	fetch := func() (string, error) {
		// A build machine has decided to fetch the model, so the offer is
		// accepted. The token, when set, goes only to Hugging Face.
		return voice.Ensure(context.Background(), &http.Client{}, os.Getenv("HF_TOKEN"),
			func(o voice.Offer) bool {
				fmt.Printf("fetching %s (%d bytes) from huggingface.co/%s\n", o.File, o.Size, o.Repo)
				return true
			}, nil)
	}
	dest, err := prepare(out, voice.ModelFile, voice.Verify, fetch)
	if err != nil {
		fmt.Fprintln(os.Stderr, "voiceprep:", err)
		os.Exit(1)
	}
	fmt.Println("speech model ready:", dest)
}

// prepare makes out/name a verified copy of the model. A file already there
// that verifies is left alone; anything else is replaced from fetch's result.
func prepare(out, name string, verify func(string) error, fetch func() (string, error)) (string, error) {
	dest := filepath.Join(out, name)
	if err := verify(dest); err == nil {
		return dest, nil
	}
	src, err := fetch()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	if err := copyFile(src, dest); err != nil {
		return "", err
	}
	if err := verify(dest); err != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("the copied model failed its checksum: %w", err)
	}
	return dest, nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// repoRoot walks up from the working directory to the module root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found: run voiceprep inside the repository")
		}
		dir = parent
	}
}
