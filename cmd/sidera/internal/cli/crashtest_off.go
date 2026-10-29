//go:build !crashtest

package cli

import "crypto/rand"

func (c *command) configureTestProcess(string) (func() error, error) {
	c.random = rand.Reader
	return func() error { return nil }, nil
}
