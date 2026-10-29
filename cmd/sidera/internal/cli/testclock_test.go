//go:build testclock

package cli

import (
	"testing"
	"time"
)

func TestClockOriginComesFromTheInjectedEnvironment(t *testing.T) {
	c := command{Config: Config{Getenv: func(name string) string {
		if name == "SIDERA_TEST_CLOCK_START_MS" {
			return "1700000000123"
		}
		return ""
	}}}
	clk, err := c.testClock()
	if err != nil {
		t.Fatal(err)
	}
	if !clk.Now().Equal(time.UnixMilli(1700000000123)) {
		t.Fatalf("clock=%v", clk.Now())
	}
}
