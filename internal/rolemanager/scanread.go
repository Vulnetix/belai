package rolemanager

import (
	"context"

	"github.com/vulnetix/belai/internal/tools"
)

// ProcessRead classifies a result, reading only as much of a Read's file as
// its content class needs (ScanForRead). Anything ScanForRead does not cover
// goes through Process unchanged.
func (p *Pipeline) ProcessRead(ctx context.Context, res tools.Result, codeFull bool) (Decision, error) {
	if sc, ok := ScanForRead(res, p, codeFull); ok {
		return p.ProcessScan(ctx, res, sc)
	}
	return p.Process(ctx, res)
}

// ScanForRead decides what the classifier reads of a Read result that carries
// its whole file (docs/architecture.md, "Tool-result trust"):
//
//   - code: only its comments and docstrings, read by the text classifier (a
//     SystemOne decision backend, else the LLM sentinel; never the ML gates).
//     A file with no comments needs no call. codeFull (classifier.code_scan
//     "full") reads the whole file through the usual classifier instead.
//   - prose: the whole file, through the text classifier when a SystemOne
//     backend answers it, else through the usual classifier.
//   - anything else: the whole file through the usual classifier.
//
// Every verdict is keyed on the file's path and digest, so a later read of the
// same bytes (another window, an offset, a warmed doc) reuses it. A file read
// without its whole bytes (over tools.WholeMaxBytes), or one the usual
// classifier would have to read beyond a default Read's size, is left to
// Process, which reads the result exactly as before.
func ScanForRead(res tools.Result, p *Pipeline, codeFull bool) (Scan, bool) {
	if res.Kind != tools.KindRead || res.Whole == "" {
		return Scan{}, false
	}
	path, _ := res.Meta["abs"].(string)
	if path == "" {
		path, _ = res.Meta["path"].(string)
	}
	class := ClassOf(path)
	sc := Scan{Text: res.Whole, FileKey: FileKey(path, res.WholeSHA256), Class: class}
	switch {
	case class == ClassCode && !codeFull:
		sc.Text = ExtractComments(path, res.Whole)
		sc.UseText = true
	case class == ClassDoc && p.TextSystemOne():
		sc.UseText = true
	}
	// The usual classifier never reads more of a file than a default Read
	// did: the ML gates refuse content past their window cap.
	if !sc.UseText && len(sc.Text) > ScanChunkBytes {
		return Scan{}, false
	}
	return sc, true
}
