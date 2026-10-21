package rolemanager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tools"
)

// Scan says what the security classifier reads of a result in place of its
// whole content, and which classifier reads it. The result's content still
// goes to the model whole, sanitised; only the verdict comes from Scan.
type Scan struct {
	// Text is what is classified. Empty means there is nothing to classify
	// (a code file with no comments), which is SAFE without a call.
	Text string
	// Text classifies with the pipeline's text classifier (a SystemOne
	// decision backend, else the LLM sentinel) rather than the ML stack.
	UseText bool
	// FileKey, when set, keys a verdict on the whole file (path and content
	// digest), so any later read of the same bytes reuses it.
	FileKey string
	// Class names what was scanned, for the activity feed.
	Class ContentClass
}

// ScanChunkBytes is the most scanned text one classifier call reads, the
// size of a default Read. Longer text is classified in overlapping chunks,
// so whole-file scanning never sends a model more than a Read did.
const ScanChunkBytes = 64 << 10

// FileKey is the whole-file verdict key for an absolute path and the hex
// SHA-256 of its bytes.
func FileKey(abs, sha string) string {
	if abs == "" || sha == "" {
		return ""
	}
	return abs + "\x00" + sha
}

// SetText sets the classifier that reads scanned text (code comments and
// prose). textSystemOne reports that it is a SystemOne decision backend, which
// is what lets prose leave the ML stack.
func (p *Pipeline) SetText(c Classifier, identity string, textSystemOne bool) {
	p.text = c
	p.textIdentity = identity
	p.textSystemOne = textSystemOne
}

// TextSystemOne reports whether a SystemOne decision backend reads scanned
// text. Prose goes to it, and session-start doc warming runs, only then.
func (p *Pipeline) TextSystemOne() bool { return p.text != nil && p.textSystemOne }

// textClassifier is the classifier scanned text goes to: the text classifier
// when set, else the LLM sentinel. It is never the ML stack.
func (p *Pipeline) textClassifier() Classifier {
	if p.text != nil {
		return p.text
	}
	return p.Classifier
}

func (p *Pipeline) fileCacheKey(scanText bool, fileKey string) string {
	id := p.identity
	if scanText {
		id = "text:" + p.textIdentity
	}
	return KeyFor(id+"\x00file", fileKey)
}

// ProcessScan classifies sc.Text and decides r on that verdict. The decision
// content is r's whole content, sanitised, as Process returns it.
func (p *Pipeline) ProcessScan(ctx context.Context, r tools.Result, sc Scan) (Decision, error) {
	clean := sanitize.Sanitize(r.Content)
	s, parsed, err := p.scanVerdict(ctx, sc, string(r.Kind))
	if err != nil {
		return Decision{}, err
	}
	if !parsed {
		return Decision{Kind: r.Kind, Action: ActionWarn, Content: clean}, nil
	}
	action := ActionWarn
	if s.IsSafe() {
		action = ActionProceed
	}
	return Decision{Kind: r.Kind, Action: action, Sentinel: s, Content: clean}, nil
}

// ScanVerdict classifies sc on its own, with no result: the session-start doc
// warming uses it to fill the cache.
func (p *Pipeline) ScanVerdict(ctx context.Context, sc Scan, subject string) (Sentinel, bool, error) {
	return p.scanVerdict(ctx, sc, subject)
}

func (p *Pipeline) scanVerdict(ctx context.Context, sc Scan, subject string) (Sentinel, bool, error) {
	if sc.FileKey != "" && p.Cache != nil {
		if s, ok := p.Cache.Get(p.fileCacheKey(sc.UseText, sc.FileKey)); ok {
			record(EventVerdictCacheHit, string(s), subject, sc.Class.String(), 0)
			return s, true, nil
		}
	}
	s, parsed, err := p.classifyScan(ctx, sc, subject)
	if err != nil || !parsed {
		return s, parsed, err
	}
	if sc.FileKey != "" && p.Cache != nil {
		_ = p.Cache.Put(p.fileCacheKey(sc.UseText, sc.FileKey), s)
	}
	return s, true, nil
}

func (p *Pipeline) classifyScan(ctx context.Context, sc Scan, subject string) (Sentinel, bool, error) {
	if !sc.UseText {
		_, s, parsed, err := p.run(ctx, sc.Text, subject)
		return s, parsed, err
	}
	clean := sanitize.Sanitize(sc.Text)
	if strings.TrimSpace(clean) == "" {
		return SentinelSafe, true, nil
	}
	key := KeyFor("text:"+p.textIdentity, clean)
	if p.Cache != nil {
		if s, ok := p.Cache.Get(key); ok {
			record(EventVerdictCacheHit, string(s), subject, sc.Class.String(), 0)
			return s, true, nil
		}
	}
	c := p.textClassifier()
	var s Sentinel
	var parsed bool
	if len(clean) > ScanChunkBytes {
		var err error
		s, parsed, err = p.classifyChunkedWith(ctx, c, clean, ScanChunkBytes)
		if err != nil {
			return "", false, err
		}
	} else {
		start := time.Now()
		raw, err := c.Classify(ctx, BuildClassifierPayload(clean))
		took := time.Since(start)
		if err != nil {
			return "", false, err
		}
		parsed = true
		if s, err = ParseSentinel(raw); err != nil {
			recordTimed(EventSecuritySentinelMalformed, "", subject, sc.Class.String(), 0, p.textLabel(), took)
			return "", false, nil
		}
		recordTimed(EventSecuritySentinel, string(s), subject, sc.Class.String(), 0, p.textLabel(), took)
	}
	if parsed && p.Cache != nil {
		_ = p.Cache.Put(key, s)
	}
	return s, parsed, nil
}

func (p *Pipeline) textLabel() string {
	if p.textIdentity != "" {
		return p.textIdentity
	}
	return p.securityModelLabel
}

// RecordDocsWarmed emits the outcome of a session-start doc warm-up: how many
// of the project's prose files were classified ahead of any Read. Never a
// path.
func RecordDocsWarmed(classified, listed int, model string, took time.Duration) {
	recordTimed(EventDocsWarmed, "warmed", "", fmt.Sprintf("classified=%d listed=%d", classified, listed), 0, model, took)
}

func docsWarmedDescription(a Activity) Description {
	return Description{
		Tone:    ToneNeutral,
		Levels:  LevelDecisions,
		Summary: "Classified the project's docs ahead of time",
		Outcome: field(a.Detail, "classified") + " of " + field(a.Detail, "listed") + " files, so reading them needs no check",
	}
}

// TextLabel names the classifier that reads scanned text, for the trace.
func (p *Pipeline) TextLabel() string { return p.textLabel() }
