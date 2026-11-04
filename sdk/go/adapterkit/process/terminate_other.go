//go:build !darwin && !linux && !windows

package process

import (
	"context"
	"fmt"
	"time"
)

func terminate(context.Context, Identity, time.Duration) error {
	return fmt.Errorf("%w: process termination", ErrUnsupported)
}
