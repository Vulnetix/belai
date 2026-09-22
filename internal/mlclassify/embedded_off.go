//go:build !signet_bert && !signet_bert_jailbreak

package mlclassify

// embeddedSpecs returns the models embedded in this build variant. The
// vanilla (untagged) binary embeds none: it classifies exactly as it did
// before the ML stack existed, through the LLM sentinel path.
func embeddedSpecs() []embeddedSpec { return nil }

func jailbreakEmbeddedSpec() (embeddedSpec, bool) { return embeddedSpec{}, false }
