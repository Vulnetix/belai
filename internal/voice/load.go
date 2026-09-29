package voice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/vulnetix/belai/internal/voice/asr"
)

// ErrNoModel is returned when the speech model is neither built in nor on disk.
var ErrNoModel = errors.New("the speech model is not downloaded")

// Embedded reports whether the speech model is built into this binary. Release
// builds carry it (the belai_voice build tag); a plain `go build` does not.
func Embedded() bool { return len(embeddedModel) > 0 }

// ModelSource says where the model comes from: "built in", the path of the
// file on disk, or "" when there is none.
func ModelSource() string {
	if Embedded() {
		return "built in"
	}
	return ModelPath()
}

// LoadModel reads the speech model into memory: the embedded copy when this
// binary has one, otherwise the file on disk. Either is checked against the
// pinned SHA-256 first, so a swapped file is refused rather than run.
func LoadModel() (*asr.Model, error) {
	if Embedded() {
		sum := sha256.Sum256(embeddedModel)
		if hex.EncodeToString(sum[:]) != pinnedSHA {
			return nil, fmt.Errorf("built-in model: %w", ErrModelChanged)
		}
		return asr.LoadBytes(embeddedModel)
	}
	path := ModelPath()
	if path == "" {
		return nil, ErrNoModel
	}
	if err := Verify(path); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return asr.Load(path)
}
