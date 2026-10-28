package fsx

import (
	"errors"
	"fmt"
	"os"
)

// CreatePrivateFile exclusively creates an empty, owner-only writable file at
// an OS path. It verifies the opened file before returning it; existing names,
// including dangling symbolic links, are never replaced. The parent must exist.
// The caller owns writing, syncing, closing and cleanup after this call succeeds.
// Network shares are not supported on Windows.
func CreatePrivateFile(path string) (*os.File, error) {
	f, err := openPrivateFile(path)
	if err != nil {
		return nil, err
	}
	return finishPrivateFile(f, verifyPrivateFile)
}

// finishPrivateFile receives only a newly created ordinary file, never a
// device. Verification runs before the caller can write any secret bytes.
func finishPrivateFile(f *os.File, verify func(*os.File) error) (*os.File, error) {
	if err := verify(f); err != nil {
		return nil, errors.Join(
			fmt.Errorf("fsx: cannot create private file %q: %w; export to stdout without --out instead", f.Name(), err),
			f.Close(), os.Remove(f.Name()),
		)
	}
	return f, nil
}
