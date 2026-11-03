//go:build !linux

package process_test

import "testing"

func checkScope(t *testing.T, _ child) {
	t.Helper()
	t.Fatal("only Linux puts a process in a systemd unit")
}
