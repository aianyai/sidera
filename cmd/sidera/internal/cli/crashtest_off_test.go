//go:build !crashtest

package cli

import (
	"crypto/rand"
	"testing"
)

func TestOrdinaryRandomnessIgnoresTestEnvironment(t *testing.T) {
	c := command{Config: Config{Getenv: func(string) string {
		t.Fatal("ordinary build read a crash-test environment variable")
		return "invalid"
	}}}
	close, err := c.configureTestProcess("daemon")
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	if c.random != rand.Reader || c.observer != nil || c.afterRecv != nil {
		t.Fatal("ordinary process installed test facilities")
	}
}
