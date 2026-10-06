// Package tags gives an indexed document searchable labels: its file type and
// metadata, and the topics it is about.
//
// Labels come from the path, the size and the document's structure through
// fixed tables, never from free text. Topics are detected two ways. The
// deterministic detector here matches a fixed vocabulary of about three
// hundred topics against the sanitised text with word and phrase tables and a
// few anchored regular expressions: it runs everywhere, is linear in the text,
// and needs no model. A decision backend can refine it (see Merge and
// Candidates): it scores the best candidates in one call, and any topic it was
// not asked about, or an answer it did not give, keeps the detector's verdict.
//
// The package is a leaf: it reads no file and calls no model, so everything it
// returns is a function of its Input.
package tags

import (
	"context"
	"sort"
)

// Version is the tag format. A document tagged under an older version is
// tagged again on the next sync, so a change to the vocabulary or the label
// tables takes effect without a manual rebuild.
const Version uint16 = 1

// MaxTopics is the most topics one document keeps.
const MaxTopics = 24

// Source says who decided a topic.
type Source uint8

const (
	// SrcNone is the zero value: not tagged yet.
	SrcNone Source = iota
	// SrcRegex is the deterministic detector.
	SrcRegex
	// SrcJev is a decision backend's score.
	SrcJev
)

// String is the short word shown in the UI.
func (s Source) String() string {
	switch s {
	case SrcRegex:
		return "patterns"
	case SrcJev:
		return "jev"
	}
	return "none"
}

// Topic is one detected topic. ID is a vocabulary id; Score is a confidence in
// [0,1].
type Topic struct {
	ID    string
	Score float32
	Src   Source
}

// Result is everything known about one document's type and subject.
type Result struct {
	// Kind is the coarse document type: source, test, doc, config, data, ci,
	// iac, dependency, scanner, build, script or text.
	Kind string
	// Lang is the language name for source, script and some config files.
	Lang string
	// Labels are the static labels (type, lang, ext, dir, size, has, ...),
	// each key:value, sorted and unique. Topic labels are not stored here; see
	// All.
	Labels []string
	// Topics are the document's topics, best first.
	Topics []Topic
	// Ver is the tag format the result was made under.
	Ver uint16
	// Src is SrcJev when a decision backend scored the topics, SrcRegex when
	// only the detector did.
	Src Source
}

// Input is what the tagger may look at: where the document is, how big it is,
// and its admitted chunk text. Rel is the path used for labels (the document's
// address without its scope), never a host path.
type Input struct {
	Rel  string
	Size int64
	// Artifact and Tool are the scanner artefact kind and tool for a
	// .vulnetix file; empty for any other document.
	Artifact string
	Tool     string
	// Records is true when each chunk is one scanner record.
	Records bool
	// Chunks is the document's admitted text, in order.
	Chunks []string
}

// Tagger tags a document. The default is Fast; the harness installs one that
// asks a decision backend as well.
type Tagger interface {
	// Tag returns the document's tags. It must not fail: with nothing better
	// it returns the deterministic result.
	Tag(ctx context.Context, in Input) Result
	// Refresh reports whether a document tagged as r should be tagged again
	// now: the format is stale, or it was tagged without a backend and one is
	// available.
	Refresh(r Result) bool
}

// Fast is the deterministic tagger: labels and the pattern detector.
type Fast struct{}

// Tag implements Tagger.
func (Fast) Tag(_ context.Context, in Input) Result { return Detect(in) }

// Refresh implements Tagger.
func (Fast) Refresh(r Result) bool { return r.Ver != Version }

// Detect is the deterministic result for in.
func Detect(in Input) Result {
	r := Result{Ver: Version, Src: SrcRegex}
	r.Kind, r.Lang, r.Labels = Labels(in)
	r.Topics = Match(in)
	return r
}

// All returns the document's searchable labels: the static ones and one
// topic:<id> per topic.
func (r Result) All() []string {
	out := make([]string, 0, len(r.Labels)+len(r.Topics))
	out = append(out, r.Labels...)
	for _, t := range r.Topics {
		out = append(out, "topic:"+t.ID)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the document carries the label (exact, case-insensitive
// on the key:value text).
func (r Result) Has(label string) bool {
	label = normLabel(label)
	for _, l := range r.All() {
		if l == label {
			return true
		}
	}
	return false
}
