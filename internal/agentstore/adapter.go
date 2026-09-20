package agentstore

import (
	"context"
	"regexp"
	"time"
)

// Source is one enumerated transcript store entry.
type Source struct {
	Agent     string
	Format    Format
	Path      string    // absolute file path
	SessionID string    // "" until resolved for multi-session stores
	Project   string    // recorded cwd, "" when unknown
	ModTime   time.Time // file mtime
}

// Turn is one attributed message in a transcript.
type Turn struct {
	Index int
	Role  string
	Text  string
	Model string
	At    time.Time
}

// Hit is one search match with full attribution.
type Hit struct {
	Agent     string
	SessionID string
	Path      string
	Project   string
	Role      string
	Turn      int
	At        time.Time
	Snippet   string
}

// MemoryHit is one memory-file search match.
type MemoryHit struct {
	Path string
	Line int
	Text string
}

// Caps bounds a scan. Every limit is explicit and reported when hit.
type Caps struct {
	MaxMatches    int
	MaxSnippet    int
	MaxTotalBytes int
	MaxFiles      int
	Deadline      time.Duration
}

// DefaultCaps returns the production caps: 200 matches, 2 KiB per snippet,
// 256 KiB total payload, 5000 files scanned, 10 s deadline.
func DefaultCaps() Caps {
	return Caps{
		MaxMatches:    200,
		MaxSnippet:    2 * 1024,
		MaxTotalBytes: 256 * 1024,
		MaxFiles:      5000,
		Deadline:      10 * time.Second,
	}
}

// Adapter parses one transcript dialect. Implementations are stateless except
// for the SQLite ones, which carry the resolved sqlite3 binary.
type Adapter interface {
	// Sources enumerates the Sources reachable from one concrete store path.
	// A one-file-per-session dialect returns a single Source; a database file
	// returns one Source per stored session.
	Sources(path string) ([]Source, error)
	// Turns reads a turn range from one Source. from is inclusive, to is
	// exclusive. A from/to of 0 means "the whole session".
	Turns(src Source, from, to int) ([]Turn, error)
	// Scan searches one Source for re and returns attributed hits, honouring
	// caps.
	Scan(ctx context.Context, src Source, re *regexp.Regexp, caps Caps) ([]Hit, error)
}

// SessionQuery is one SearchSessions request.
type SessionQuery struct {
	Re          *regexp.Regexp
	Agent       string // registry name; "" = all present
	AllProjects bool
	Project     string // substring match on recorded cwd; default current workdir
	Role        string // "", "user", or "assistant"
	Since       time.Time
	Until       time.Time
	PromptsOnly bool
	MaxMatches  int
}

// SourceCount is one agent's contribution to the SearchSessions header.
type SourceCount struct {
	Agent string
	Files int
}

// SkippedAgent is one non-present agent in the SearchSessions header.
type SkippedAgent struct {
	Agent  string
	Reason string
}

// SessionSearch is the assembled SearchSessions result.
type SessionSearch struct {
	Sources      []SourceCount
	Skipped      []SkippedAgent
	Hits         []Hit
	Truncated    bool
	DeadlineHit  bool
	FilesScanned int
}

// ReadRequest is one ReadSession request.
type ReadRequest struct {
	Agent     string
	SessionID string // prefix-resolvable
	From, To  int    // turn indices; To 0 means "to the end"
}

// SessionRead is the assembled ReadSession result.
type SessionRead struct {
	Agent     string
	SessionID string
	Path      string
	Project   string
	Turns     []Turn
	Truncated bool
}

// MemoryQuery is one SearchMemory request.
type MemoryQuery struct {
	Re           *regexp.Regexp
	Agent        string // registry name; "" = all present
	File         string // exact path from the probed memory set; "" = search all
	ContextLines int
	MaxMatches   int
}

// MemorySearch is the assembled SearchMemory result.
type MemorySearch struct {
	Files     []string
	Hits      []MemoryHit
	Content   string // whole-file content when File was requested
	Truncated bool
}
