//go:build !windows

package update

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// ownedByCaller says whether the file belongs to the user running the process (the
// effective user), and otherwise whose it is.
func ownedByCaller(_ string, fi fs.FileInfo) (owned bool, owner string, err error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, "", fmt.Errorf("the system reports no owner")
	}
	if int(st.Uid) == os.Geteuid() {
		return true, "", nil
	}
	return false, fmt.Sprintf("user %d (you are user %d)", st.Uid, os.Geteuid()), nil
}

// reachableByOthers says whether the group or everyone has any access to the file.
func reachableByOthers(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o077 != 0 }
