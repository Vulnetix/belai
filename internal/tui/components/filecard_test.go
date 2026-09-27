package components

import (
	"strings"
	"testing"
)

func TestFileCardRendersMetadata(t *testing.T) {
	meta := FileMeta{
		Path:     "/work/main.go",
		IsDir:    false,
		FileSize: 1234,
		DiskSize: 4096,
		MIMEType: "text/plain; charset=utf-8",
		Tokens:   313,
		Trust:    "trusted",
	}
	out, _ := FileCard(meta, 60)
	for _, want := range []string{"main.go", "~313 tok", "text/plain", "1.2 kB", "disk 4.0 kB", "trusted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("file card missing %q:\n%s", want, out)
		}
	}
}

func TestFileCardDirectoryRendersSummary(t *testing.T) {
	meta := FileMeta{
		Path:      "/work/docs",
		IsDir:     true,
		TotalSize: 15000,
		Tokens:    3755,
		Trust:     "added root",
		FileCount: 7,
	}
	out, _ := FileCard(meta, 60)
	for _, want := range []string{"docs", "7 files", "14.6 kB", "~3755 tok", "added root"} {
		if !strings.Contains(out, want) {
			t.Fatalf("directory card missing %q:\n%s", want, out)
		}
	}
}

func TestFileCardCompact(t *testing.T) {
	meta := FileMeta{
		Path:     "main.go",
		FileSize: 1234,
		MIMEType: "text/plain",
		Tokens:   313,
	}
	out := FileCardCompact(meta, 80)
	if !strings.Contains(out, "main.go") {
		t.Fatalf("compact card missing name: %q", out)
	}
	if !strings.Contains(out, "~313 tok") {
		t.Fatalf("compact card missing tokens: %q", out)
	}
}

func TestFileIcon(t *testing.T) {
	cases := []struct {
		path  string
		isDir bool
		mime  string
		want  string
	}{
		{"docs", true, "", "📁"},
		{"main.go", false, "", "🐹"},
		{"photo.png", false, "", "🖼"},
		{"song.mp3", false, "", "🎵"},
		{"plain.txt", false, "text/plain", "📄"},
		{"image", false, "image/webp", "🖼"},
	}
	for _, tc := range cases {
		got := fileIcon(FileMeta{Path: tc.path, IsDir: tc.isDir, MIMEType: tc.mime})
		if got != tc.want {
			t.Fatalf("fileIcon(%q, dir=%v, %q) = %q, want %q", tc.path, tc.isDir, tc.mime, got, tc.want)
		}
	}
}

func TestFormatAttachSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 kB"},
		{1536, "1.5 kB"},
		{2 << 20, "2.0 MB"},
		{3 << 30, "3.0 GB"},
	}
	for _, tc := range cases {
		if got := formatAttachSize(tc.n); got != tc.want {
			t.Fatalf("formatAttachSize(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
