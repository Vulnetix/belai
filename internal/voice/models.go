package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/vulnetix/belai/internal/localinfer"
)

// The speech model belai runs. The SHA-256 is pinned in addition to the one
// Hugging Face reports, so a replaced upstream file or a tampered copy in a
// shared cache is refused rather than run.
const (
	ModelRepo   = "ggerganov/whisper.cpp"
	ModelFile   = "ggml-tiny.en-q5_1.bin"
	ModelSize   = 32166155
	ModelSHA256 = "c77c5766f1cef09b6b7d47f21b546cbddd4157886b3b5d6d4f709e91e66c7c2b"
)

// pinnedSHA is ModelSHA256; tests substitute the hash of their fixture.
var pinnedSHA = ModelSHA256

// ErrDeclined is returned when the user declines the model download.
var ErrDeclined = errors.New("voice model download declined")

// ErrModelChanged is returned when a model file does not hash to ModelSHA256.
var ErrModelChanged = errors.New("voice model does not match the pinned checksum")

// Offer describes a download the user is asked to confirm.
type Offer struct {
	Repo, File string
	Size       int64
	Dest       string
}

// ModelPath returns the model file when it is already on disk, else "".
func ModelPath() string { return localinfer.FindModelFile(ModelRepo, ModelFile) }

// Verify checks that path hashes to ModelSHA256.
func Verify(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != pinnedSHA {
		return ErrModelChanged
	}
	return nil
}

// Ensure returns a verified model path. A model already on disk is used
// without any network call. Otherwise confirm is asked with the size
// Hugging Face reports, and only a yes downloads (resumable, checked against
// Hugging Face's SHA-256, retried once when that check fails). A file that
// fails the pinned checksum is refused, and removed when belai downloaded it.
func Ensure(ctx context.Context, client *http.Client, token string, confirm func(Offer) bool, progress localinfer.Progress) (string, error) {
	if p := ModelPath(); p != "" {
		if err := Verify(p); err != nil {
			return "", fmt.Errorf("%s: %w", p, err)
		}
		return p, nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	info, err := localinfer.RemoteInfo(ctx, client, ModelRepo, ModelFile, token)
	if err != nil {
		return "", err
	}
	dest, _ := localinfer.ModelsDir()
	if confirm == nil || !confirm(Offer{Repo: ModelRepo, File: ModelFile, Size: info.Size, Dest: dest}) {
		return "", ErrDeclined
	}
	var path string
	for attempt := 0; attempt < 2; attempt++ {
		path, err = localinfer.DownloadFile(ctx, client, ModelRepo, ModelFile, token, info, progress)
		var de *localinfer.DownloadError
		if err == nil || ctx.Err() != nil || !errors.As(err, &de) || de.Class != localinfer.DownloadChecksum {
			break
		}
	}
	if err != nil {
		return "", err
	}
	if err := Verify(path); err != nil {
		_ = localinfer.RemoveModelFile(ModelRepo, ModelFile)
		return "", err
	}
	return path, nil
}
