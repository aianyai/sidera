//go:build !darwin && !linux && !windows

package process

import "fmt"

// Only macOS, Linux and Windows have a process table here. Every call fails with
// ErrUnsupported and names the operation: an adapter must not guess about
// processes it cannot see.

func lookup(int, bool) (Process, bool, error) {
	return Process{}, false, fmt.Errorf("%w: process lookup", ErrUnsupported)
}

func list() ([]Process, error) {
	return nil, fmt.Errorf("%w: process listing", ErrUnsupported)
}
