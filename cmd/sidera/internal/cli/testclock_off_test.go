//go:build !testclock

package cli

import "testing"

func TestOrdinaryClockIgnoresTestEnvironment(t *testing.T) {
	c := command{Config: Config{Getenv: func(string) string { t.Fatal("build without testclock read a test clock variable"); return "invalid" }}}
	clk, err := c.testClock()
	if err != nil || clk != nil {
		t.Fatalf("clock=%v error=%v", clk, err)
	}
}
