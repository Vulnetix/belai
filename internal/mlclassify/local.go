package mlclassify

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"

	"github.com/nlpodyssey/cybertron/pkg/tasks/textclassification/bert"
	"github.com/vulnetix/signet/internal/rolemanager"
)

// embeddedSpec describes one model embedded in this binary variant.
type embeddedSpec struct {
	// id is the HuggingFace model id.
	id string
	// phase is the gate the model implements.
	phase Phase
	// fsys is the model directory (vocab.txt, tokenizer_config.json,
	// config.json, spago_model.bin).
	fsys fs.FS
	// attack is the label index (into the id-ordered label list) that means
	// "attack". It is pinned at build time by the modelprep golden test and
	// must never be guessed at runtime.
	attack int
	// sentinel is the verdict the gate emits when it fires.
	sentinel rolemanager.Sentinel
}

// localModel runs one embedded BERT sequence classifier in-process.
type localModel struct {
	spec      embeddedSpec
	threshold float64
	m         *bert.TextClassification
	tokFn     tokenizeFunc
}

// loadedLocal is a shared, already-loaded embedded model. Loading a BERT model
// and its tokenizer is expensive (~20 MB read + parameter wiring), so it is
// done once per model id and shared across every pipeline that uses it.
type loadedLocal struct {
	m     *bert.TextClassification
	tokFn tokenizeFunc
}

var (
	loadedMu    sync.Mutex
	loadedCache = map[string]*loadedLocal{}
)

// loadLocalModel loads (or returns the cached) embedded model for a spec.
func loadLocalModel(spec embeddedSpec) (*loadedLocal, error) {
	loadedMu.Lock()
	defer loadedMu.Unlock()
	if l, ok := loadedCache[spec.id]; ok {
		return l, nil
	}
	dir, err := extractEmbedded(spec)
	if err != nil {
		return nil, err
	}
	m, err := bert.LoadTextClassification(dir)
	if err != nil {
		return nil, fmt.Errorf("load embedded model %q: %w", spec.id, err)
	}
	tokFn, err := newWordPieceTokenizer(filepath.Join(dir, "vocab.txt"))
	if err != nil {
		return nil, fmt.Errorf("load vocabulary for %q: %w", spec.id, err)
	}
	l := &loadedLocal{m: m, tokFn: tokFn}
	loadedCache[spec.id] = l
	return l, nil
}

func newLocalGate(mc ModelConfig, spec embeddedSpec) (gate, error) {
	l, err := loadLocalModel(spec)
	if err != nil {
		return nil, err
	}
	return &localModel{
		spec:      spec,
		threshold: mc.threshold(),
		m:         l.m,
		tokFn:     l.tokFn,
	}, nil
}

func (g *localModel) phase() Phase { return g.spec.phase }

func (g *localModel) tokenizer() tokenizeFunc { return g.tokFn }

func (g *localModel) fire(ctx context.Context, window string) (rolemanager.Sentinel, float64, error) {
	resp, err := g.m.Classify(ctx, window)
	if err != nil {
		return "", 0, fmt.Errorf("classify %s: %w", g.spec.id, err)
	}
	if g.spec.attack < 0 || g.spec.attack >= len(g.m.Labels) {
		return "", 0, fmt.Errorf("model %q attack label %d out of range %v", g.spec.id, g.spec.attack, g.m.Labels)
	}
	attackName := g.m.Labels[g.spec.attack]
	score := 0.0
	for i, l := range resp.Labels {
		if l == attackName {
			score = resp.Scores[i]
			break
		}
	}
	if score >= g.threshold {
		return g.spec.sentinel, score, nil
	}
	return rolemanager.SentinelSafe, score, nil
}
