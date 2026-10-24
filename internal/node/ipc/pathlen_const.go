package ipc

// The longest socket path, in bytes without the terminating NUL, that each
// platform accepts. Both are here so that tests on any machine can check both.
const (
	darwinSocketPathLimit = 103
	linuxSocketPathLimit  = 107
)
