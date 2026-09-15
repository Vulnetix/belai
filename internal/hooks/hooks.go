package hooks

import (
	"fmt"
	"os"

	"github.com/vulnetix/signet/internal/posture"
)

// ValidateWithPosture runs ValidateHook and applies the hook_invalid posture.
// Under enforce (default) a validation error is returned. Under warn the
// error is printed to stderr and nil is returned. Under ignore validation is
// skipped entirely.
func ValidateWithPosture(h Hook, pol posture.Policy) (*Hook, error) {
	if pol.Level(posture.HookInvalid) == posture.Ignore {
		return &h, nil
	}
	validated, err := ValidateHook(h)
	if err != nil {
		if pol.Level(posture.HookInvalid) == posture.Warn {
			fmt.Fprintf(os.Stderr, "signet: warning: invalid hook: %v\n", err)
			return nil, nil
		}
		return nil, err
	}
	return validated, nil
}
