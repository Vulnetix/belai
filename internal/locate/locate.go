// Package locate finds the files a question is about without reading the
// repository into a model. It builds an inventory of the files that may be
// looked at (dependency, hidden, ignored, sensitive, binary and oversized
// files never are), narrows it lexically, and lets a Rater (a decision
// backend) reorder directories and then files. The result is a ranked list of
// paths with a line number each: facts the harness computed, never file text.
//
// The package is deterministic and does no network I/O. A Rater is the only
// way a model-like judgement enters, and it sees labels the caller builds: a
// path and size always, and declaration names only when the caller allows
// previews. When the Rater is missing or fails the lexical order stands.
package locate

import "context"

// Thresholds a rated file is judged against. They mirror the decision-backend
// jobs: a file at or above HitAt is a hit, one at or above LeadAt is a lead
// (worth reading if the hits fall short), and the rest are dropped.
const (
	HitAt  = 0.50
	LeadAt = 0.25
)

// Limits on the work one search does.
const (
	// DirsRemote and DirsLocal bound how many directories are rated.
	DirsRemote = 48
	DirsLocal  = 12
	// FilesRemote and FilesLocal bound how many files are rated.
	FilesRemote = 160
	FilesLocal  = 24
	// DefaultMaxHits bounds the hits returned.
	DefaultMaxHits = 25
	// LexicalOnlyHits is how many files a search returns when no Rater
	// answered.
	LexicalOnlyHits = 12
)

// Item is one directory or file put to a Rater.
type Item struct {
	// ID is unique within one call.
	ID string
	// Label is what the Rater is shown. The caller of Rate turns it into a
	// decision-safe value; it is never file text unless previews are allowed.
	Label string
}

// Rater rates items against a query. Scores are 0 to 1. An item missing from
// Scores has no answer: it is unknown, not low.
type Rater interface {
	Rate(ctx context.Context, stage Stage, query string, items []Item) (scores map[string]float64, err error)
}

// Stage says what is being rated.
type Stage string

const (
	StageDirs  Stage = "dirs"
	StageFiles Stage = "files"
)

// Options tune one search.
type Options struct {
	// Rater refines the lexical order; nil means lexical only.
	Rater Rater
	// Local says the Rater answers one question per request, so fewer, smaller
	// batches are put to it.
	Local bool
	// Previews allows declaration names in file labels.
	Previews bool
	// MaxHits bounds the result; zero means DefaultMaxHits.
	MaxHits int
	// Exclude, when set, removes a file (by inventory path) before anything
	// is scored, labelled or returned: the caller's way to keep out a file
	// it has reason not to look at, such as one whose read was withheld.
	Exclude func(path string) bool
}

// Hit is one ranked file.
type Hit struct {
	// Path is relative to the inventory root, with forward slashes.
	Path string
	// Score is 0 to 1: the Rater's score, or the normalised lexical score when
	// no Rater answered for this file.
	Score float64
	// Line is the line of the declaration that best matches the query, or 0.
	Line int
	// Rated is true when a Rater scored the file.
	Rated bool
	// Lead marks a file below HitAt but at or above LeadAt.
	Lead bool
}

// Result is the outcome of one search.
type Result struct {
	Hits []Hit
	// DirsRated and FilesRated count what the Rater was asked about.
	DirsRated, FilesRated int
	// Unknown counts items the Rater gave no answer for.
	Unknown int
	// Lexical is true when no Rater answer was used.
	Lexical bool
}
