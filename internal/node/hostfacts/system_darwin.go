package hostfacts

import "golang.org/x/sys/unix"

// readSystem asks the kernel through sysctl, which is what sw_vers reads too,
// without starting a process.
func readSystem() system {
	var s system
	version, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		s.problems = append(s.problems, "kern.osproductversion: "+err.Error())
	} else if version != "" {
		s.label = "macOS " + version
	}
	build, err := unix.Sysctl("kern.osversion")
	if err != nil {
		s.problems = append(s.problems, "kern.osversion: "+err.Error())
	}
	s.build = build
	return s
}
