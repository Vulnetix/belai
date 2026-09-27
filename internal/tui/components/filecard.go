package components

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// FileMeta holds the metadata shown for an attached file or directory card.
type FileMeta struct {
	Path      string
	IsDir     bool
	FileSize  int64
	DiskSize  int64
	TotalSize int64 // recursive size for directories
	MIMEType  string
	Tokens    int
	Trust     string
	FileCount int
}

// fileCardMinWidth is the narrowest card that still renders a readable body.
const fileCardMinWidth = 28

// FileCard renders a metadata card for one attached file or directory. It
// returns the rendered panel and a provenance line map the same shape as
// Panel.Render.
func FileCard(meta FileMeta, width int) (string, LineMap) {
	width = max(width, fileCardMinWidth)

	name := filepath.Base(meta.Path)
	if name == "" || name == "." {
		name = meta.Path
	}
	title := fileIcon(meta) + " " + name
	metaLine := fileMetaLine(meta)
	trustLine := "trust: " + meta.Trust

	body := strings.Join([]string{metaLine, trustLine}, "\n")
	return Panel{
		Title:  title,
		Meta:   formatFileCardTokens(meta.Tokens),
		Body:   body,
		Width:  width,
		Accent: fileCardAccent(meta),
	}.Render()
}

// FileCardCompact renders a one-line summary of a file or directory, used
// when many attachments are shown together and vertical space is limited.
func FileCardCompact(meta FileMeta, width int) string {
	width = max(width, fileCardMinWidth)
	inner := width - 4
	name := filepath.Base(meta.Path)
	if name == "" || name == "." {
		name = meta.Path
	}
	line := fileIcon(meta) + " " + name
	if meta.IsDir {
		line += fmt.Sprintf(" · %d files · %s", meta.FileCount, formatAttachSize(meta.TotalSize))
	} else {
		line += fmt.Sprintf(" · %s · %s", meta.MIMEType, formatAttachSize(meta.FileSize))
	}
	line += " · " + formatFileCardTokens(meta.Tokens)
	if visibleLen(line) > inner {
		line = truncateRunes(line, inner)
	}
	return line
}

// fileIcon picks a glyph for the file based on its kind.
func fileIcon(meta FileMeta) string {
	if meta.IsDir {
		return "📁"
	}
	ext := strings.ToLower(filepath.Ext(meta.Path))
	switch ext {
	case ".go":
		return "🐹"
	case ".py":
		return "🐍"
	case ".js", ".ts", ".jsx", ".tsx":
		return "⚡"
	case ".md", ".markdown", ".mdx":
		return "📝"
	case ".json", ".yaml", ".yml", ".toml":
		return "⚙"
	case ".html", ".htm", ".css":
		return "🌐"
	case ".sh", ".bash", ".zsh":
		return "⌨"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return "🖼"
	case ".mp3", ".wav", ".ogg", ".flac":
		return "🎵"
	case ".mp4", ".mov", ".avi", ".mkv":
		return "🎬"
	case ".zip", ".tar", ".gz", ".bz2", ".7z", ".rar":
		return "🗜"
	}
	if strings.HasPrefix(meta.MIMEType, "image/") {
		return "🖼"
	}
	if strings.HasPrefix(meta.MIMEType, "video/") {
		return "🎬"
	}
	if strings.HasPrefix(meta.MIMEType, "audio/") {
		return "🎵"
	}
	return "📄"
}

// fileCardAccent chooses a frame colour to distinguish directories from files.
func fileCardAccent(meta FileMeta) lipgloss.TerminalColor {
	if meta.IsDir {
		return lipgloss.TerminalColor(ColorAmber)
	}
	ext := strings.ToLower(filepath.Ext(meta.Path))
	switch ext {
	case ".go", ".py", ".js", ".ts", ".jsx", ".tsx", ".rs", ".c", ".cpp", ".h", ".java":
		return lipgloss.TerminalColor(ColorTeal)
	case ".md", ".markdown", ".mdx":
		return lipgloss.TerminalColor(ColorTealSoft)
	}
	return lipgloss.TerminalColor(ColorMuted)
}

// fileMetaLine builds the primary metadata body line for a card.
func fileMetaLine(meta FileMeta) string {
	if meta.IsDir {
		return fmt.Sprintf("%d files · %s", meta.FileCount, formatAttachSize(meta.TotalSize))
	}
	line := meta.MIMEType
	if meta.FileSize > 0 {
		line += fmt.Sprintf(" · %s", formatAttachSize(meta.FileSize))
	}
	if meta.DiskSize > 0 && meta.DiskSize != meta.FileSize {
		line += fmt.Sprintf(" · disk %s", formatAttachSize(meta.DiskSize))
	}
	if strings.HasPrefix(line, " · ") {
		line = strings.TrimPrefix(line, " · ")
	}
	return line
}

// formatFileCardTokens renders a token count with the leading tilde that the
// compact and full cards share.
func formatFileCardTokens(n int) string {
	return "~" + FormatTokens(n) + " tok"
}

// formatAttachSize renders a byte count as a short human-readable string.
func formatAttachSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
