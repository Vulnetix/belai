package mlclassify

import (
	"fmt"

	"github.com/nlpodyssey/cybertron/pkg/tokenizers/wordpiecetokenizer"
	"github.com/nlpodyssey/cybertron/pkg/vocabulary"
)

// newWordPieceTokenizer builds the windowing tokenizer from a BERT vocab.txt.
// Both phase models are uncased, so text is lowercased (ASCII) before
// tokenization to match what cybertron does internally.
func newWordPieceTokenizer(vocabPath string) (tokenizeFunc, error) {
	vocab, err := vocabulary.NewFromFile(vocabPath)
	if err != nil {
		return nil, fmt.Errorf("load vocabulary: %w", err)
	}
	tok := wordpiecetokenizer.New(vocab)
	return func(text string) []span {
		text = lowerASCII(text)
		pairs := tok.Tokenize(text)
		out := make([]span, len(pairs))
		for i, p := range pairs {
			out[i] = span{start: p.Offsets.Start, end: p.Offsets.End}
		}
		return out
	}, nil
}

// lowerASCII lowercases ASCII runes in place, mirroring BERT's uncased
// do_lower_case without pulling in a full unicode case table.
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
