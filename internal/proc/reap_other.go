//go:build !linux

package proc

import (
	"context"
	"time"
)

// ReapOrphans does nothing off Linux: only Linux has child subreapers, and the
// hosted sandboxes that run out of process slots are Linux containers.
func ReapOrphans(ctx context.Context, every time.Duration, logf func(format string, args ...any)) {
}
