package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/tools"
)

// Session-start doc warming: while a SystemOne decision backend answers the
// security checks, the prose files git tracks in the project are classified
// in the background, in large batches, so a later Read of any of them is
// answered from the file verdict cache with no classifier call. Nothing is
// shown to a model. docs/architecture.md, "Tool-result trust".
const (
	// WarmDocsMax is the most files one warm-up classifies.
	WarmDocsMax = 500
	// WarmDocsMaxBytes is the largest file it classifies, a default Read.
	WarmDocsMaxBytes = rolemanager.ScanChunkBytes
	// WarmDocsTimeout bounds the whole warm-up.
	WarmDocsTimeout = 60 * time.Second
	// warmDocsInFlight is how many checks wait at once. The SystemOne batcher
	// gathers them into requests, so this is the batch width, not a count of
	// concurrent requests.
	warmDocsInFlight = 250
)

// WarmDocs classifies the prose files git tracks under root through pipe's
// text classifier and records a file verdict for each. It does nothing, and
// returns 0, unless that classifier is a SystemOne decision backend. It
// returns how many files were classified.
func WarmDocs(ctx context.Context, pipe *rolemanager.Pipeline, root string) int {
	if pipe == nil || !pipe.TextSystemOne() || root == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, WarmDocsTimeout)
	defer cancel()
	start := time.Now()
	files := warmDocFiles(ctx, root)
	var done atomic.Int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, warmDocsInFlight)
	for _, abs := range files {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(abs string) {
			defer wg.Done()
			defer func() { <-sem }()
			b, err := os.ReadFile(abs)
			if err != nil || len(b) > WarmDocsMaxBytes || bytes.IndexByte(b, 0) >= 0 {
				return
			}
			sum := sha256.Sum256(b)
			sc := rolemanager.Scan{
				Text:    string(b),
				UseText: true,
				FileKey: rolemanager.FileKey(abs, hex.EncodeToString(sum[:])),
				Class:   rolemanager.ClassDoc,
			}
			if _, parsed, err := pipe.ScanVerdict(ctx, sc, string(tools.KindRead)); err == nil && parsed {
				done.Add(1)
			}
		}(abs)
	}
	wg.Wait()
	n := int(done.Load())
	if len(files) > 0 {
		rolemanager.RecordDocsWarmed(n, len(files), pipe.TextLabel(), time.Since(start))
	}
	return n
}

// warmDocFiles lists the prose files git tracks under root, skipping
// dependency, hidden and sensitive paths and anything over the size cap.
func warmDocFiles(ctx context.Context, root string) []string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, rel := range bytes.Split(out, []byte{0}) {
		if len(files) == WarmDocsMax {
			break
		}
		r := string(rel)
		if r == "" || rolemanager.ClassOf(r) != rolemanager.ClassDoc {
			continue
		}
		if _, skip := locate.SkipPath(r); skip {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(r))
		if info, err := os.Stat(abs); err != nil || !info.Mode().IsRegular() || info.Size() > WarmDocsMaxBytes {
			continue
		}
		files = append(files, abs)
	}
	return files
}
