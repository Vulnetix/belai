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
//go:embed assets/jackhhao_jailbreak-classifier
var phase2FS embed.FS

func jailbreakEmbeddedSpec() (embeddedSpec, bool) {
	sub, err := fs.Sub(phase2FS, "assets/jackhhao_jailbreak-classifier")
	if err != nil {
		// Static embed path: this cannot fail at runtime.
		panic(err)
	}
	return embeddedSpec{
		id:       "jackhhao/jailbreak-classifier",
		phase:    Phase2,
		fsys:     sub,
		attack:   phase2AttackLabel,
		sentinel: rolemanager.SentinelJailbreak,
	}, true
}
