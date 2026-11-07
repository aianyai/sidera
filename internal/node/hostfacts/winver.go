package hostfacts

import (
	"fmt"
	"strings"
)

// windows11FirstBuild is the first build number of Windows 11. Windows 11
// reports version 10.0 like Windows 10, so the build number tells them apart.
const windows11FirstBuild = 22000

// windowsLabel names a Windows release from its version numbers, followed by
// its display version ("24H2") when it has one. It does not take the product
// name from the registry, which still says "Windows 10" on Windows 11.
func windowsLabel(major, minor, build uint32, displayVersion string) string {
	var label string
	switch {
	case major == 10 && build >= windows11FirstBuild:
		label = "Windows 11"
	case major == 10:
		label = "Windows 10"
	default:
		label = fmt.Sprintf("Windows %d.%d", major, minor)
	}
	if v := strings.TrimSpace(displayVersion); v != "" {
		label += " " + v
	}
	return label
}

// windowsBuild is "major.minor.build", followed by ".revision" when the
// registry has an update build revision (UBR).
func windowsBuild(major, minor, build uint32, ubr *uint32) string {
	s := fmt.Sprintf("%d.%d.%d", major, minor, build)
	if ubr != nil {
		s += fmt.Sprintf(".%d", *ubr)
	}
	return s
}
