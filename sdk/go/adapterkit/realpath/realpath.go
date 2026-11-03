// Package realpath resolves a path to the form the platform itself reports for it.
// A program that resolves its own working directory or its arguments ends up with
// that form, so a path compared with one of its paths has to be resolved the same
// way first.
package realpath

// Resolve returns the absolute path of the existing file or directory at path, as
// the platform resolves it. On Windows: every symbolic link and junction followed,
// each name in its on-disk case and long (not 8.3) form, through
// GetFinalPathNameByHandle. Elsewhere: filepath.EvalSymlinks of the absolute path.
// This is the path a program that resolves its own working directory or its
// arguments ends up with. An error when path does not exist.
func Resolve(path string) (string, error) {
	return resolve(path)
}
