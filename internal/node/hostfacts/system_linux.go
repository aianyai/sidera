package hostfacts

import (
	"golang.org/x/sys/unix"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// osReleaseLimit bounds the read of an os-release file, which is a few hundred
// bytes; a larger file is not one.
const osReleaseLimit = 64 << 10

// readSystem names the distribution from os-release and takes the kernel
// release for the build. In a container os-release describes the image and the
// kernel is the host's.
func readSystem() system {
	var s system
	label, err := readLinuxLabel()
	if err != nil {
		s.problems = append(s.problems, "os-release: "+err.Error())
	}
	s.label = label
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		s.problems = append(s.problems, "uname: "+err.Error())
	} else {
		s.build = unix.ByteSliceToString(uts.Release[:])
	}
	return s
}

// readLinuxLabel reads os-release through the read side of fsx opened at the
// root of the file system. ReadFileLimit follows links, which /etc/os-release
// usually is (to ../usr/lib/os-release).
func readLinuxLabel() (string, error) {
	root, err := fsx.OpenOSReader("/")
	if err != nil {
		return "Linux", err
	}
	return linuxLabel(func(path string) ([]byte, error) {
		return root.ReadFileLimit(path, osReleaseLimit)
	})
}
