//go:build windows

package update

import "io/fs"

// ownedByCaller reports every file as the caller's: there is no owner check on
// Windows. The trap the check guards on Unix, programs and directories that belong
// to root after a run under sudo and that the user cannot then use, does not exist
// here: the files this program creates take the access list of the directory they are
// created in, so the user keeps full control of what an elevated run writes. Comparing
// the owner with the user of the token would also refuse files an elevated process
// created, which the system gives to the Administrators group, not to the user, in
// every later session.
func ownedByCaller(string, fs.FileInfo) (owned bool, owner string, err error) {
	return true, "", nil
}

// reachableByOthers is false on Windows: who may enter a directory is its access list,
// not a mode, and the directories this program makes private carry one that names the
// user alone (see fsx.MakeRoot).
func reachableByOthers(fs.FileInfo) bool { return false }
