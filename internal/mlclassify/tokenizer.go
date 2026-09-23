package mlclassify

import (
	"fmt"
	"strings"

	"github.com/nlpodyssey/cybertron/pkg/tokenizers/wordpiecetokenizer"
	"github.com/nlpodyssey/cybertron/pkg/vocabulary"
)

// newWordPieceTokenizer builds the windowing tokenizer from a BERT vocab.txt.
// Both phase models are uncased, so text is lowercased before tokenization
// with the same full Unicode mapping cybertron applies internally (an
// ASCII-only lowering counted non-ASCII upper-case words differently).
// Lower-casing is rune-for-rune, so rune offsets still index the original.
func newWordPieceTokenizer(vocabPath string) (tokenizeFunc, error) {
	vocab, err := vocabulary.NewFromFile(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("load vocabulary: %w", err)
	}
	tok := wordpiecetokenizer.New(vocab)
	return func(text string) []span {
		text = strings.ToLower(text)
		pairs := tok.Tokenize(text)
		out := make([]span, len(pairs))
		for i, p := range pairs {
			out[i] = span{start: p.Offsets.Start, end: p.Offsets.End}
		}
		return out
	}, nil
}
