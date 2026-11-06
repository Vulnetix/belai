//go:build !linux

package egress

import "syscall"

func dieWithParent() error { return nil }

func childAttr() *syscall.SysProcAttr { return nil }
