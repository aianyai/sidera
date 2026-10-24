//go:build unix

package ipc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// osFiles is the real file system below the data directory.
type osFiles struct{ root string }

func (f osFiles) Lstat(name string) (fs.FileInfo, error) {
	return os.Lstat(filepath.Join(f.root, filepath.FromSlash(name)))
}

func (o Options) fileView(home string) lstater {
	if o.files != nil {
		return o.files
	}
	return osFiles{root: home}
}

// ownerUID is the user that owns the file, as the kernel recorded it.
func ownerUID(fi fs.FileInfo) (uint32, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// The directory and socket checks below are the same for the daemon that is
// about to listen and the client that is about to connect: both rely on the
// directory being closed to everyone else. Names are relative to the data
// directory, as in a read-only view of it.

func (a Address) dirName() string {
	d, _ := a.kind.socketDir()
	return d
}

func (a Address) socketName() string { return a.dirName() + "/" + string(a.kind) + ".sock" }

// checkSocketDirectory requires the socket's directory to be a real directory
// (not a symbolic link), owned by euid, with permissions exactly 0700.
func checkSocketDirectory(files lstater, addr Address, euid uint32) error {
	path := filepath.Join(addr.home, addr.dirName())
	fi, err := files.Lstat(addr.dirName())
	if err != nil {
		if os.IsNotExist(err) {
			return &PeerCheckError{Failure: FailDirectoryMissing, Path: path, Detail: "the socket directory does not exist", Err: err}
		}
		return &PeerCheckError{Failure: FailStat, Path: path, Detail: "cannot inspect the socket directory", Err: err}
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return &PeerCheckError{Failure: FailDirectorySymlink, Path: path, Detail: "the socket directory is a symbolic link, want a directory"}
	case !fi.IsDir():
		return &PeerCheckError{Failure: FailDirectoryNotDir, Path: path, Detail: "the socket directory is not a directory"}
	}
	uid, ok := ownerUID(fi)
	if !ok {
		return &PeerCheckError{Failure: FailStat, Path: path, Detail: "cannot read the owner of the socket directory"}
	}
	if uid != euid {
		return &PeerCheckError{Failure: FailDirectoryOwner, Path: path,
			Detail: fmt.Sprintf("the socket directory is owned by uid %d, want the current user (uid %d)", uid, euid)}
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		return &PeerCheckError{Failure: FailDirectoryMode, Path: path,
			Detail: fmt.Sprintf("the socket directory has mode %04o, want 0700", perm)}
	}
	return nil
}

// checkSocketFile requires the socket to be a socket (not a symbolic link or
// any other file) owned by euid. A missing socket is reported as
// FailSocketMissing so that the caller can tell it from a failed check.
func checkSocketFile(files lstater, addr Address, euid uint32) error {
	fi, err := files.Lstat(addr.socketName())
	if err != nil {
		if os.IsNotExist(err) {
			return &PeerCheckError{Failure: FailSocketMissing, Path: addr.name, Detail: "the socket does not exist", Err: err}
		}
		return &PeerCheckError{Failure: FailStat, Path: addr.name, Detail: "cannot inspect the socket", Err: err}
	}
	if fi.Mode()&fs.ModeSocket == 0 {
		return &PeerCheckError{Failure: FailSocketNotSocket, Path: addr.name,
			Detail: fmt.Sprintf("the file at the socket path is %s, want a socket", fi.Mode().Type())}
	}
	uid, ok := ownerUID(fi)
	if !ok {
		return &PeerCheckError{Failure: FailStat, Path: addr.name, Detail: "cannot read the owner of the socket"}
	}
	if uid != euid {
		return &PeerCheckError{Failure: FailSocketOwner, Path: addr.name,
			Detail: fmt.Sprintf("the socket is owned by uid %d, want the current user (uid %d)", uid, euid)}
	}
	return nil
}
