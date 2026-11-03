// Package realpath resolves a path to the form a program that resolves its own
// working directory or its arguments ends up with, so that a path compared with one
// of its paths can be resolved the same way first. Symbolic links are resolved on
// every platform; the case and the long form of the names are the platform's own
// only on Windows.
package realpath

// Resolve returns the absolute path of the existing file or directory at path. On
// Windows: every symbolic link and junction followed, each name in its on-disk case
// and long (not 8.3) form, through GetFinalPathNameByHandle, which is the path the
// platform reports. Elsewhere: filepath.EvalSymlinks of the absolute path, which
// follows symbolic links but keeps the case of the names as given, so on a
// case-insensitive macOS volume "/USERS/me" stays as written although the platform
// reports "/Users/me" for a working directory. An error when path does not exist.
func Resolve(path string) (string, error) {
	return resolve(path)
}
