//go:build unix

package tui

import (
	"os"
	"syscall"
)

// diskSize returns the number of bytes the file occupies on storage. On Unix
// the standard st_blocks count is in 512-byte units, so that is what we use;
// when the underlying stat is unavailable the logical size is returned.
func diskSize(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	sys := info.Sys()
	if sys == nil {
		return info.Size()
	}
	if st, ok := sys.(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return info.Size()
}
