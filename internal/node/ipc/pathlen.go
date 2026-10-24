package ipc

// The longest socket path, in bytes without the terminating NUL, that each
// platform accepts. macOS measured 103 (a 104-byte path fails with "invalid
// argument"; unix(4) says only "at most 104 characters"); Linux documents a
// 108-byte sun_path that includes the NUL. Both are defined here, whatever the
// platform, so that tests on any machine can check both.
const (
	darwinSocketPathLimit = 103
	linuxSocketPathLimit  = 107
)

// checkSocketPathLength is the one place that decides whether a socket path
// fits: Locate calls it, and init, daemon start and client connect all obtain
// their addresses from Locate. The check is on the absolute path in bytes.
func checkSocketPathLength(path string, limit int) error {
	if len(path) > limit {
		return &PathTooLongError{Path: path, Length: len(path), Limit: limit}
	}
	return nil
}
