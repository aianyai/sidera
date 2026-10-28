//go:build windows

package fsx

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openPrivateFile(path string) (*os.File, error) {
	sd, err := privateSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
		// InheritHandle stays zero: the caller alone owns the writable handle.
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: err}
	}
	// READ_CONTROL is required by GetSecurityInfo. No sharing keeps another
	// handle from writing the empty file while its security is being checked.
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE|windows.READ_CONTROL,
		0, sa, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	if err := privateDiskFile(f); err != nil {
		// CreateFile also opens devices, even with CREATE_NEW. A device path
		// does not name an empty file we created, so never remove it.
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func privateDiskFile(f *os.File) error {
	typ, err := windows.GetFileType(windows.Handle(f.Fd()))
	if err != nil {
		return err
	}
	if typ != windows.FILE_TYPE_DISK {
		return ErrNotRegular
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrNotRegular
	}
	return nil
}

func verifyPrivateFile(f *os.File) error {
	return verifyPrivateFileOnVolume(f, privateFileVolumeFlags)
}

func privateFileVolumeFlags(h windows.Handle) (uint32, error) {
	var flags uint32
	err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, &flags, nil, 0)
	return flags, err
}

// The volume query is kept at the OS boundary so failures and a volume that
// discards ACLs can be exercised without writing secrets to such a volume.
func verifyPrivateFileOnVolume(f *os.File, volumeFlags func(windows.Handle) (uint32, error)) error {
	h := windows.Handle(f.Fd())
	flags, err := volumeFlags(h)
	if err != nil {
		return fmt.Errorf("cannot verify volume ACL support (network shares are unsupported): %w", err)
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return errors.New("volume does not preserve and enforce ACLs")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if !ownerOnlyDescriptor(sd) {
		return errors.New("file is not owner-only")
	}
	return nil
}
