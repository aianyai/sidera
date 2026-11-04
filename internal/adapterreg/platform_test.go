package adapterreg_test

import (
	"testing"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
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

// A package can be installed when the protocol versions it implements and the
// ones the node speaks have a version in common, whatever the width of either
// range.
func TestAPackageSpeaksTheProtocolWhenTheRangesShareAVersion(t *testing.T) {
	node := adapterproto.Range{Min: 2, Max: 4}
	for _, tc := range []struct {
		name string
		pkg  adapterproto.Range
		want adapterproto.Range
		ok   bool
	}{
		{"the same range", adapterproto.Range{Min: 2, Max: 4}, adapterproto.Range{Min: 2, Max: 4}, true},
		{"a wider range", adapterproto.Range{Min: 1, Max: 9}, adapterproto.Range{Min: 2, Max: 4}, true},
		{"a narrower range", adapterproto.Range{Min: 3, Max: 3}, adapterproto.Range{Min: 3, Max: 3}, true},
		{"touching at the top", adapterproto.Range{Min: 4, Max: 7}, adapterproto.Range{Min: 4, Max: 4}, true},
		{"touching at the bottom", adapterproto.Range{Min: 1, Max: 2}, adapterproto.Range{Min: 2, Max: 2}, true},
		{"only older versions", adapterproto.Range{Min: 1, Max: 1}, adapterproto.Range{}, false},
		{"only newer versions", adapterproto.Range{Min: 5, Max: 6}, adapterproto.Range{}, false},
	} {
		m := man()
		m.Protocol = tc.pkg
		got, ok := adapterreg.SharedProtocol(node, m)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: SharedProtocol = %+v, %v; want %+v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
