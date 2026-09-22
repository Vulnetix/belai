package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nlpodyssey/cybertron/pkg/converter"
	"github.com/nlpodyssey/cybertron/pkg/tasks/textclassification/bert"
)

// convertToSpago converts the downloaded PyTorch weights in dir to the spaGO
// binary format cybertron loads at runtime.
func convertToSpago(dir string) error {
	return converter.Convert[float32](dir, true)
}

// verify loads the converted model and runs the golden set, returning the
// label index that means "attack". It fails when the benign strings score on
// the attack class, so a silently miscalibrated model can never ship.
func verify(dir string, spec modelSpec) (int, error) {
	m, err := bert.LoadTextClassification(dir)
	if err != nil {
		return 0, fmt.Errorf("load converted model: %w", err)
	}

	// The attack class is whichever label scores highest, on average, across
	// the known-attack strings.
	attackSum := make([]float64, len(m.Labels))
	attackN := 0
	for _, s := range spec.attackGolden {
		r, err := m.Classify(context.Background(), s)
		if err != nil {
			return 0, fmt.Errorf("classify attack golden %q: %w", truncate(s, 40), err)
		}
		for i, l := range r.Labels {
			for j, label := range m.Labels {
				if l == label {
					attackSum[j] += r.Scores[i]
				}
			}
		}
		attackN++
	}
	if attackN == 0 {
		return 0, fmt.Errorf("model %s has no attack golden strings", spec.id)
	}
	best := 0
	for i := range attackSum {
		if attackSum[i] > attackSum[best] {
			best = i
		}
	}

	for _, s := range spec.attackGolden {
		r, err := m.Classify(context.Background(), s)
		if err != nil {
			return 0, err
		}
		for i, l := range r.Labels {
			if l == m.Labels[best] {
				fmt.Printf("  attack  %q -> %s score %.4f\n", truncate(s, 40), l, r.Scores[i])
			}
		}
	}

	for _, s := range spec.benignGolden {
		r, err := m.Classify(context.Background(), s)
		if err != nil {
			return 0, fmt.Errorf("classify benign golden %q: %w", truncate(s, 40), err)
		}
		score := 0.0
		for i, l := range r.Labels {
			if l == m.Labels[best] {
				score = r.Scores[i]
			}
		}
		fmt.Printf("  benign  %q -> %s score %.4f\n", truncate(s, 40), m.Labels[best], score)
		if score >= 0.5 {
			return 0, fmt.Errorf("benign golden %q scored %.3f on the attack label %q; the golden set is not discriminative", truncate(s, 40), score, m.Labels[best])
		}
	}
	return best, nil
}

// emit copies the four files bert.LoadTextClassification needs into out.
func emit(out, work string) error {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	for _, f := range []string{"vocab.txt", "tokenizer_config.json", "config.json", "spago_model.bin"} {
		data, err := os.ReadFile(filepath.Join(work, f))
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if err := os.WriteFile(filepath.Join(out, f), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
