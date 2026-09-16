//go:build windows

package machineprobe

// diskFreeMiB is not measured on Windows; 0 reads as "unknown".
func diskFreeMiB() int64 { return 0 }
