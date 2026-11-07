// Package hostfacts reads what this machine is, for the node to publish about
// itself: its host name, its platform, and the operating system it runs, as a
// human label and as a machine version string.
//
// It reads through the standard library, golang.org/x/sys and, for files, the
// read-only part of fsx, never runs a program and uses no cgo, so it cross-compiles to every release target. A
// value it cannot read is empty; it never fails.
package hostfacts

import (
	"os"
	"runtime"
	"strings"
	"sync"
)

// Facts is what the machine says about itself. A field is "" when it cannot be
// read. Facts does not check its fields against any display rule: whoever
// publishes them does.
type Facts struct {
	// HostName is the first DNS label of the host name, such as "imac" for
	// "imac.local".
	HostName string
	// Platform is the operating system and architecture as Go spells them, such
	// as "darwin/arm64".
	Platform string
	// System is the operating system as a person names it, such as
	// "macOS 27.0.1", "Ubuntu 24.04.1 LTS" or "Windows 11 24H2".
	System string
	// SystemBuild is the machine version of the operating system: the build
	// number on macOS, the kernel release on Linux and
	// "major.minor.build[.revision]" on Windows.
	SystemBuild string
	// Problems says, one line each, why a field is empty or less exact than it
	// could be. It is for a debug log, not for the owner or a peer.
	Problems []string
}

// system is the part of Facts that does not change while the process runs.
type system struct {
	label, build string
	problems     []string
}

// readSystemOnce is the one read of the operating system; readSystem is
// defined per platform in system_<goos>.go.
var readSystemOnce = sync.OnceValue(readSystem)

// Read returns the facts of this machine. The operating system is read once per
// process, because it does not change while the daemon runs; the host name is
// read on every call, because the owner can rename the machine.
func Read() Facts {
	sys := readSystemOnce()
	f := Facts{
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		System:      sys.label,
		SystemBuild: sys.build,
		Problems:    append([]string(nil), sys.problems...),
	}
	name, err := os.Hostname()
	if err != nil {
		f.Problems = append(f.Problems, "host name: "+err.Error())
	}
	f.HostName = firstLabel(name)
	return f
}

// firstLabel is the first DNS label of a host name: "imac" for "imac.local".
// The domain a network adds to a name changes with the network; the label does
// not.
func firstLabel(hostName string) string {
	label, _, _ := strings.Cut(strings.TrimSpace(hostName), ".")
	return strings.TrimSpace(label)
}
