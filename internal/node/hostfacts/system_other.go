//go:build !darwin && !linux && !windows

package hostfacts

// readSystem reads nothing: only macOS, Linux and Windows are release targets.
func readSystem() system { return system{} }
