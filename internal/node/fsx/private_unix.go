//go:build unix

package fsx

import (
	"errors"
	"os"
)

func openPrivateFile(path string) (*os.File, error) {
	// O_EXCL refuses every existing final name, including devices and links.
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

func verifyPrivateFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	if !ownerOnly(f.Name(), info) {
		return errors.New("file is not owner-only")
	}
	return nil
}
