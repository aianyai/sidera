package release

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	revision := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		name     string
		stamped  string
		settings []debug.BuildSetting
		want     Info
	}{
		{"development build outside a checkout", "", nil, Info{Version: DevVersion}},
		{"release build", "0.8.0", []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}},
			Info{Version: "0.8.0", Commit: "0123456789ab"}},
		{"modified checkout", "", []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "true"}},
			Info{Version: DevVersion, Commit: "0123456789ab", Modified: true}},
		{"short revision is kept whole", "", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}, Info{Version: DevVersion, Commit: "abc"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := resolve(c.stamped, c.settings)
			got.GoVersion = ""
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		info Info
		want string
	}{
		{Info{Version: "0.8.0", Commit: "abc", GoVersion: "go1.26"}, "sidera 0.8.0 (commit abc, go1.26)"},
		{Info{Version: DevVersion, Commit: "abc", Modified: true, GoVersion: "go1.26"}, "sidera 0.0.0-dev (commit abc, modified, go1.26)"},
		{Info{Version: DevVersion, GoVersion: "go1.26"}, "sidera 0.0.0-dev (commit unknown, go1.26)"},
	} {
		if got := c.info.Describe("sidera"); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
