//go:build windows

package realpath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// The flags of GetFinalPathNameByHandle that this package uses; both are zero and
// x/sys does not name them.
const (
	fileNameNormalized = 0x0
	volumeNameDOS      = 0x0
)

func resolve(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return "", err
	}
	// No access is asked for: the handle only names the file. Backup semantics let
	// CreateFile open a directory.
	h, err := windows.CreateFile(name, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "realpath", Path: path, Err: err}
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), fileNameNormalized|volumeNameDOS)
		if err != nil {
			return "", &os.PathError{Op: "realpath", Path: path, Err: err}
		}
		if int(n) < len(buf) {
			final, err := trimExtendedPrefix(windows.UTF16ToString(buf[:n]))
			if err != nil {
				return "", &os.PathError{Op: "realpath", Path: path, Err: err}
			}
			return final, nil
		}
		// n is the size needed, terminator included.
		buf = make([]uint16, n)
	}
}

// trimExtendedPrefix turns the path GetFinalPathNameByHandle returns into the form
// programs use: `\\?\C:\dir` becomes `C:\dir` and `\\?\UNC\srv\share\dir` becomes
// `\\srv\share\dir`. A volume that has no drive letter (`\\?\Volume{...}\dir`) has
// no such form and is an error.
func trimExtendedPrefix(p string) (string, error) {
	rest, ok := strings.CutPrefix(p, `\\?\`)
	if !ok {
		return "", fmt.Errorf("unexpected final path %q: no extended-length prefix", p)
	}
	if unc, ok := strings.CutPrefix(rest, `UNC\`); ok {
		return `\\` + unc, nil
	}
	if len(rest) >= 2 && rest[1] == ':' && (len(rest) == 2 || rest[2] == '\\') {
		return rest, nil
	}
	return "", errors.New("the path is on a volume that has no drive letter: " + p)
}
