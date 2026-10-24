package keys

import (
	"errors"
	"fmt"
	"io/fs"
	"path"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/recfile"
)

// maxFileSize is the size of the largest file of keys/: a record file at its
// limits. The signed records (the certificate, the self-revocation) are smaller.
const maxFileSize = recfile.MaxHeaderLen + recfile.MaxBodyLen

// statOwnerOnly reports the file as missing, a directory, or too broad; nil when
// it is a file that only its owner can reach.
func statOwnerOnly(r fsx.Reader, p string) (fsx.Info, error) {
	info, err := r.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return info, fileError(p, ErrMissing, "")
	}
	if err != nil {
		return info, &FileError{Path: p, Err: fmt.Errorf("cannot read the file's permissions: %w", err)}
	}
	if !info.OwnerOnly {
		return info, fileError(p, ErrPermissionsTooBroad, "")
	}
	return info, nil
}

// readOwnerOnly reads a file of keys/, after checking that only its owner can
// reach it (a key that others could read is not read at all) and that it is not
// larger than any file of keys/ can be.
func readOwnerOnly(r fsx.Reader, p string) ([]byte, error) {
	info, err := statOwnerOnly(r, p)
	if err != nil {
		return nil, err
	}
	if info.IsDir {
		return nil, fileError(p, ErrCorrupt, "it is a directory")
	}
	if info.Size > maxFileSize {
		return nil, fileError(p, ErrCorrupt, "it is %d bytes, over the %d of the largest file", info.Size, maxFileSize)
	}
	data, err := r.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fileError(p, ErrMissing, "")
	}
	if err != nil {
		return nil, &FileError{Path: p, Err: fmt.Errorf("cannot read the file: %w", err)}
	}
	return data, nil
}

// CheckPermissions refuses a keys directory, or a file in it, that anyone but
// the owner can reach, naming it: on Unix any group or other permission bit, on
// Windows a DACL that grants access to anyone but the owner, SYSTEM and
// Administrators. It never changes a permission. A directory that does not exist
// has nothing to refuse.
func CheckPermissions(r fsx.Reader) error {
	info, err := r.Stat(Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return &FileError{Path: Dir, Err: fmt.Errorf("cannot read the permissions: %w", err)}
	}
	if !info.OwnerOnly {
		return fileError(Dir, ErrPermissionsTooBroad, "")
	}
	entries, err := r.ReadDir(Dir)
	if err != nil {
		return &FileError{Path: Dir, Err: fmt.Errorf("cannot list the directory: %w", err)}
	}
	for _, e := range entries {
		p := path.Join(Dir, e.Name)
		if _, err := statOwnerOnly(r, p); err != nil && !errors.Is(err, ErrMissing) {
			return err
		}
	}
	return nil
}

// corrupt marks the failure to read a file as damage. The error it carries never
// repeats the content of the file: the decoders say which check failed, not what
// they found, because a key file is secret.
func corrupt(p string, err error) error {
	return fileError(p, ErrCorrupt, "%v", err)
}
