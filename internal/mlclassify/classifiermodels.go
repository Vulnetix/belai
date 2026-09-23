package mlclassify

// classifierModel is one curated entry in the supported classifier model
// catalogue. The catalogue is the single source of truth for the model ids the
// /model classifier picker offers and the attack-label names the remote
// HuggingFace gate compares against when one of those models is selected.
type classifierModel struct {
	id string
	// phase is the local gate the model implements (Phase1 saturation or
	// Phase2 jailbreak). It is informational: the gate's sentinel is still
	// derived from the phase argument, never from this field.
	phase Phase
	// attackLabel is the classifier label that means "attack" on this model,
	// as returned by the HuggingFace inference API.
	attackLabel string
	// blurb is the one-line efficacy/benefit note the /model picker shows next
	// to the model id.
	blurb string
}

// classifierModels is the curated catalogue of supported BERT classifier
// models. The five ids are exactly the set documented in docs/role-manager.md
// under "Phase-2 jailbreak model selection" plus the phase-1 saturation model.
var classifierModels = []classifierModel{
	{
		id:          "GuardrailsAI/prompt-saturation-attack-detector",
		phase:       Phase1,
		attackLabel: "LABEL_1", // no id2label; LABEL_1 is the saturation-attack class
		blurb:       "prompt-saturation gate · precise on tool output",
	},
	{
		id:          "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak",
		phase:       Phase2,
		attackLabel: "unsafe", // id2label: 0 = "safe", 1 = "unsafe"
		blurb:       "jailbreak gate · safe/unsafe labels",
	},
	{
		id:          "leomaurodesenv/bert-base-uncased-jailbreakv-28k",
		phase:       Phase2,
		attackLabel: "unsafe", // id2label: "safe"/"unsafe"
		blurb:       "jailbreak gate · high reported accuracy",
	},
	{
		// No id2label; the HuggingFace inference API reports the default
		// "LABEL_0"/"LABEL_1" orientation, and index 1 is the attack class
		// (consistent with every other BERT jailbreak classifier here).
		id:          "hurtmongoose/bert-base-detect-jailbreak",
		phase:       Phase2,
		attackLabel: "LABEL_1",
		blurb:       "jailbreak gate · self-contained vocab, F1 0.89",
	},
	{
		id:          "hurtmongoose/jailbreak-bert-base-uncased",
		phase:       Phase2,
		attackLabel: "jailbreak", // id2label: "benign"/"jailbreak"
		blurb:       "jailbreak gate · benign/jailbreak labels",
	},
}

// ClassifierModelIDs returns the curated classifier model ids, in catalogue
// order. It is the list the /model classifier picker offers for the
// huggingface provider.
func ClassifierModelIDs() []string {
	out := make([]string, 0, len(classifierModels))
	for _, m := range classifierModels {
		out = append(out, m.id)
	}
	return out
}

// IsKnownClassifierModel reports whether id is one of the curated classifier
// models. Consumers use it to validate a model the user selected for a local
// phase gate, so a remote model can be resolved from the catalogue instead of
// guessed.
func IsKnownClassifierModel(id string) bool {
	_, ok := classifierModelByID(id)
	return ok
}

// AttackLabelFor returns the attack-label name for a curated classifier model.
// It lets the remote HuggingFace gate and the security-classifier resolver
// map a model id to the label that means "attack" without embedding a
// build-time golden-test mapping.
func AttackLabelFor(id string) (string, bool) {
	m, ok := classifierModelByID(id)
	if !ok {
		return "", false
	}
	return m.attackLabel, true
}

// BlurbFor returns the one-line efficacy/benefit note for a curated classifier
// model, and whether the id is in the catalogue. It is the text the /model
// picker shows to the right of the model id.
func BlurbFor(id string) (string, bool) {
	m, ok := classifierModelByID(id)
	if !ok || m.blurb == "" {
		return "", false
	}
	return m.blurb, true
}

// classifierModelByID returns the catalogue entry for a model id.
func classifierModelByID(id string) (classifierModel, bool) {
	for _, m := range classifierModels {
		if m.id == id {
			return m, true
		}
	}
	return classifierModel{}, false
}
