//go:build testclock

package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
)

func (c *command) testClock() (clock.Clock, error) {
	value := c.Getenv("SIDERA_TEST_CLOCK_START_MS")
	if value == "" {
		return nil, nil
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("SIDERA_TEST_CLOCK_START_MS: %w", err)
	}
	start := time.UnixMilli(ms)
	if start.IsZero() {
		return nil, fmt.Errorf("SIDERA_TEST_CLOCK_START_MS cannot be the zero time")
	}
	return clocktest.NewFake(start), nil
}
