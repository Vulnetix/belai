//go:build belai_voice

package voice

import _ "embed"

// embeddedModel is the speech model built into this binary. The file is
// fetched by tools/voiceprep before a tagged build and is gitignored, so the
// weights never enter the repository.
//
//go:embed assets/ggml-tiny.en-q5_1.bin
var embeddedModel []byte
