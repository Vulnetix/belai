package mlclassify

import (
	"testing"
)

// TestClassifierModelsCatalogue pins the five curated classifier model ids and
// their documented attack labels, so the /model picker and the remote gate
// resolve from one source of truth.
func TestClassifierModelsCatalogue(t *testing.T) {
	wantIDs := []string{
		"GuardrailsAI/prompt-saturation-attack-detector",
		"leomaurodesenv/bert-base-uncased-trustairlab-jailbreak",
		"leomaurodesenv/bert-base-uncased-jailbreakv-28k",
		"hurtmongoose/bert-base-detect-jailbreak",
		"hurtmongoose/jailbreak-bert-base-uncased",
	}
	got := ClassifierModelIDs()
	if len(got) != len(wantIDs) {
		t.Fatalf("ClassifierModelIDs() = %v, want %v", got, wantIDs)
	}
	for i, id := range wantIDs {
		if got[i] != id {
			t.Fatalf("ClassifierModelIDs()[%d] = %q, want %q", i, got[i], id)
		}
		if !IsKnownClassifierModel(id) {
			t.Fatalf("IsKnownClassifierModel(%q) = false, want true", id)
		}
	}

	wantLabels := map[string]string{
		"GuardrailsAI/prompt-saturation-attack-detector":         "LABEL_1",
		"leomaurodesenv/bert-base-uncased-trustairlab-jailbreak": "unsafe",
		"leomaurodesenv/bert-base-uncased-jailbreakv-28k":        "unsafe",
		"hurtmongoose/bert-base-detect-jailbreak":                "LABEL_1",
		"hurtmongoose/jailbreak-bert-base-uncased":               "jailbreak",
	}
	for id, want := range wantLabels {
		got, ok := AttackLabelFor(id)
		if !ok {
			t.Fatalf("AttackLabelFor(%q) = ok=false, want true", id)
		}
		if got != want {
			t.Fatalf("AttackLabelFor(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestIsKnownClassifierModelRejectsUnknown(t *testing.T) {
	for _, id := range []string{"", "jackhhao/jailbreak-classifier", "other/model", "typesafe/jev-1"} {
		if IsKnownClassifierModel(id) {
			t.Fatalf("IsKnownClassifierModel(%q) = true, want false", id)
		}
		if _, ok := AttackLabelFor(id); ok {
			t.Fatalf("AttackLabelFor(%q) = ok=true, want false", id)
		}
	}
}
