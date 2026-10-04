//go:build !linux

package vaultenv

// HardenProcess is a no-op off Linux, where the sandbox backend is sandbox-exec.
func HardenProcess() error { return nil }
