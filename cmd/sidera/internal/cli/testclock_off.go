//go:build !testclock

package cli

import "github.com/aianyai/sidera/internal/node/clock"

func (c *command) testClock() (clock.Clock, error) { return nil, nil }
