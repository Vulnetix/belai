//go:build belai_bert || belai_bert_jailbreak

package mlclassify

import (
	"embed"
	"io/fs"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// phase1Files embeds the converted phase-1 prompt-saturation model. The
// directory is populated by tools/modelprep before a tagged build; it is
// gitignored so the weights never enter the repository.
//
//go:embed assets/GuardrailsAI_prompt-saturation-attack-detector
var phase1FS embed.FS

func phase1EmbeddedSpec() embeddedSpec {
	sub, err := fs.Sub(phase1FS, "assets/GuardrailsAI_prompt-saturation-attack-detector")
	if err != nil {
		// Static embed path: this cannot fail at runtime.
		panic(err)
	}
	return embeddedSpec{
		id:       "GuardrailsAI/prompt-saturation-attack-detector",
		phase:    Phase1,
		fsys:     sub,
		attack:   phase1AttackLabel,
		sentinel: rolemanager.SentinelPromptInjection,
	}
}

// embeddedSpecs returns the models embedded in this build variant. The
// jailbreak variant additionally appends the phase-2 model when present.
func embeddedSpecs() []embeddedSpec {
	specs := []embeddedSpec{phase1EmbeddedSpec()}
	if jb, ok := jailbreakEmbeddedSpec(); ok {
		specs = append(specs, jb)
	}
	return specs
}
