package hostfacts

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// readSystem takes the version numbers from ntdll, which answers truthfully to a
// program without a compatibility manifest where GetVersionEx would say 6.2. The
// registry adds the display version and the update build revision when it has
// them; the registry's product name is not read, because it says "Windows 10" on
// Windows 11. RtlGetNtVersionNumbers masks the flag bits off the build number
// itself.
func readSystem() system {
	major, minor, build := windows.RtlGetNtVersionNumbers()
	var s system
	var displayVersion string
	var ubr *uint32

	// The 64-bit view, so a 32-bit build of the program reads the same key.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		s.problems = append(s.problems, "registry: "+err.Error())
	} else {
		defer k.Close()
		if v, _, err := k.GetStringValue("DisplayVersion"); err == nil {
			displayVersion = v
		} else if !errors.Is(err, registry.ErrNotExist) {
			s.problems = append(s.problems, "DisplayVersion: "+err.Error())
		}
		if v, _, err := k.GetIntegerValue("UBR"); err == nil {
			u := uint32(v)
			ubr = &u
		} else if !errors.Is(err, registry.ErrNotExist) {
			s.problems = append(s.problems, "UBR: "+err.Error())
		}
	}
	s.label = windowsLabel(major, minor, build, displayVersion)
	s.build = windowsBuild(major, minor, build, ubr)
	return s
}
