package fakeruntime

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// readProcessInfoFile shares delete access with the publisher: MoveFileEx can
// expose the complete new name before it closes its DELETE-access handle.
func readProcessInfoFile(path string) ([]byte, error) {
	name, err := filepath.Abs(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// Keep the long-path support of os.ReadFile when opening the handle directly.
	if !strings.HasPrefix(name, `\\?\`) {
		if strings.HasPrefix(name, `\\`) {
			name = `\\?\UNC\` + name[2:]
		} else {
			name = `\\?\` + name
		}
	}
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	return io.ReadAll(f)
}
