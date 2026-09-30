package tui

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/imageguard"
	"github.com/vulnetix/belai/internal/projectregistry"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/transcript"
)

// attachMeta holds the metadata shown for a resolved @file or @directory.
type attachMeta struct {
	FileSize  int64
	DiskSize  int64
	MIMEType  string
	Tokens    int
	Trust     string
	FileCount int   // recursive file count for directories
	TotalSize int64 // recursive byte total for directories

	// Width and Height are the admitted pixel size of an image attachment.
	Width  int
	Height int
}

// SetBodyTokens returns m with Tokens recomputed from body, so the estimate
// reflects the sanitised or processed version that reaches the model.
func (m attachMeta) SetBodyTokens(body string) attachMeta {
	m.Tokens = transcript.EstimateTokens(transcript.Message{Role: "user", Content: body})
	return m
}

// fileAttachmentMeta computes metadata for a resolved file attachment.
func fileAttachmentMeta(path, body string) attachMeta {
	info, err := os.Stat(path)
	m := attachMeta{MIMEType: "unknown", Trust: trustStatusForPath(path)}
	if err == nil && !info.IsDir() {
		m.FileSize = info.Size()
		m.DiskSize = diskSize(info)
		m.MIMEType = detectMIMEType(path)
		m.Tokens = transcript.EstimateTokens(transcript.Message{Role: "user", Content: body})
	}
	return m
}

// dirAttachmentMeta computes metadata for a resolved directory attachment.
func dirAttachmentMeta(path, body string) attachMeta {
	m := attachMeta{MIMEType: "directory", Trust: trustStatusForPath(path)}
	m.Tokens = transcript.EstimateTokens(transcript.Message{Role: "user", Content: body})
	m.FileCount, m.TotalSize = dirSummary(path)
	return m
}

// detectMIMEType returns a content type for path. It prefers the extension
// mapping because it is fast and deterministic; only when the extension is
// unknown does it peek at the file header.
func detectMIMEType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return "unknown"
	}
	return http.DetectContentType(buf[:n])
}

// dirSummary walks dir and returns the recursive count of regular files and
// their summed logical sizes. Errors on individual files are ignored so a
// single unreadable entry does not hide the rest of the summary.
func dirSummary(dir string) (int, int64) {
	var count int
	var size int64
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		count++
		size += info.Size()
		return nil
	})
	return count, size
}

// trustStatusForPath looks up path in the project registry and reports
// whether it is a trusted project root. Workspace dirs adopted after first
// launch are reported as "added root"; paths with no registry record fall
// back to "added root" because they are inside a confirmed session root.
func trustStatusForPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = path
	}
	reg, err := projectregistry.Load()
	if err != nil {
		return "unknown"
	}
	for _, e := range reg.All() {
		if e.Path == path || e.Path == resolved {
			if e.Trusted {
				return "trusted"
			}
			return "not trusted"
		}
	}
	return "added root"
}

// attachmentFileCountLabel renders a count with the right singular/plural.
func attachmentFileCountLabel(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// imageAttachmentMeta computes metadata for an admitted image attachment. The
// token figure is the estimate for the re-encoded PNG that is actually sent.
func imageAttachmentMeta(path string, adm imageguard.Image) attachMeta {
	m := attachMeta{MIMEType: detectMIMEType(path), Trust: trustStatusForPath(path), Width: adm.Width, Height: adm.Height}
	if info, err := os.Stat(path); err == nil {
		m.FileSize = info.Size()
		m.DiskSize = diskSize(info)
	}
	m.Tokens = run.ImageTokens(adm.PNG)
	return m
}
