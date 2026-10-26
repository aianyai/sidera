package adapterreg_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// A package runs on a platform when its manifest lists the pair exactly: the
// operating system alone or the architecture alone is not the platform.
func TestAPackageRunsOnTheExactPlatformsItLists(t *testing.T) {
	m := man()
	m.Platforms = []manifest.Platform{{OS: "linux", Arch: "amd64"}, {OS: "darwin", Arch: "arm64"}}
	for _, tc := range []struct {
		goos, goarch string
		want         bool
	}{
		{"linux", "amd64", true},
		{"darwin", "arm64", true},
		{"linux", "arm64", false},
		{"darwin", "amd64", false},
		{"windows", "amd64", false},
		{"", "", false},
	} {
		if got := adapterreg.RunsOn(m, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("RunsOn(%q, %q) = %v, want %v", tc.goos, tc.goarch, got, tc.want)
		}
	}
	if adapterreg.RunsOn(manifest.Manifest{}, "linux", "amd64") {
		t.Error("a manifest that lists no platform runs somewhere")
	}
}
