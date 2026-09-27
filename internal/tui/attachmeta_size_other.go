//go:build !unix

package tui

import "os"

// diskSize returns the number of bytes the file occupies on storage. On
// platforms without a st_blocks field we fall back to the logical file size.
func diskSize(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}
