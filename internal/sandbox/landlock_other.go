//go:build !linux

package sandbox

import "errors"

var errLandlockLinuxOnly = errors.New("Landlock is Linux only")

func landlockABI() (int, error) { return 0, errLandlockLinuxOnly }

func landlockProbe(string) (int, error) { return 0, errLandlockLinuxOnly }

func applyLandlock(llPlan, int) error { return errLandlockLinuxOnly }

func dieWithParent() error { return errLandlockLinuxOnly }

func execProcess([]string) error { return errLandlockLinuxOnly }
