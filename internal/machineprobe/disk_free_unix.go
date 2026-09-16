//go:build unix

package machineprobe

import "syscall"

// diskFreeMiB reports the free space on the global state directory's
// filesystem, best-effort. It returns 0 when the statfs syscall fails.
func diskFreeMiB() int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize) / (1024 * 1024)
}
