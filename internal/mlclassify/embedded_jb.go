//go:build signet_bert_jailbreak

package mlclassify

import (
	"embed"
	"io/fs"

	"github.com/vulnetix/signet/internal/rolemanager"
)

// phase2Files embeds the converted phase-2 jailbreak model. The directory is
// populated by tools/modelprep before a tagged build; it is gitignored so the
// weights never enter the repository.
//
//go:embed assets/leomaurodesenv_bert-base-uncased-trustairlab-jailbreak
var phase2FS embed.FS

func jailbreakEmbeddedSpec() (embeddedSpec, bool) {
	sub, err := fs.Sub(phase2FS, "assets/leomaurodesenv_bert-base-uncased-trustairlab-jailbreak")
	if err != nil {
		// Static embed path: this cannot fail at runtime.
		panic(err)
	}
	return embeddedSpec{
		id:       "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak",
		phase:    Phase2,
		fsys:     sub,
		attack:   phase2AttackLabel,
		sentinel: rolemanager.SentinelJailbreak,
	}, true
}
