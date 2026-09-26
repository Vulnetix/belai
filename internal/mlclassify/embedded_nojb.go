//go:build belai_bert && !belai_bert_jailbreak

package mlclassify

// jailbreakEmbeddedSpec reports no phase-2 model in the plain guardrails
// variant.
func jailbreakEmbeddedSpec() (embeddedSpec, bool) { return embeddedSpec{}, false }
