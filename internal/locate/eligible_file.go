package locate

import "path"

// EligibleFile applies the inventory's by-name and by-size rules to one file
// that was named directly rather than found by a walk: a dotfile, a credential
// or key store and a binary type are refused, as is a file over MaxFileBytes.
// reason is one of the Skip constants when ok is false. It never touches the
// disk; the caller still refuses symlinks and checks the bytes with IsText.
func EligibleFile(name string, size int64) (reason string, ok bool) {
	if why, skip := skipFile(path.Base(name)); skip {
		return why, false
	}
	if size > MaxFileBytes {
		return SkipLarge, false
	}
	return "", true
}
