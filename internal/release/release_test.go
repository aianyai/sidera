package release

import (
	"bytes"
	"encoding/json"
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	revision := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		name                                    string
		stamped, distribution, commit, modified string
		settings                                []debug.BuildSetting
		want                                    Info
	}{
		{"development build outside a checkout", "", "", "", "", nil, Info{Version: DevVersion}},
		{"release build", "0.8.0", "", "", "", []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}},
			Info{Version: "0.8.0", Commit: "0123456789ab"}},
		{"modified checkout", "", "", "", "", []debug.BuildSetting{{Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "true"}},
			Info{Version: DevVersion, Commit: "0123456789ab", Modified: true}},
		{"short revision is kept whole", "", "", "", "", []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}, Info{Version: DevVersion, Commit: "abc"}},
		{"stated commit wins over the recorded one", "0.8.0", "", revision, "true", []debug.BuildSetting{{Key: "vcs.revision", Value: "ffff"}, {Key: "vcs.modified", Value: "false"}},
			Info{Version: "0.8.0", Commit: "0123456789ab", Modified: true}},
		{"archive release", "0.8.0", DistributionArchive, "", "", nil, Info{Version: "0.8.0", Distribution: DistributionArchive}},
		{"distribution is independent of the commit", "0.8.0", DistributionArchive, revision, "", nil,
			Info{Version: "0.8.0", Distribution: DistributionArchive, Commit: "0123456789ab"}},
		{"stated commit of a clean tree", "0.8.0", "", revision, "", []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}},
			Info{Version: "0.8.0", Commit: "0123456789ab"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := resolve(c.stamped, c.distribution, c.commit, c.modified, c.settings)
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

func TestInfoJSONOmitsAnUnstatedDistribution(t *testing.T) {
	for _, c := range []struct {
		info Info
		want string
	}{
		{Info{Version: "0.8.0", Distribution: DistributionArchive, GoVersion: "go1.26"}, `{"version":"0.8.0","distribution":"archive","modified":false,"go_version":"go1.26"}`},
		{Info{Version: DevVersion, GoVersion: "go1.26"}, `{"version":"0.0.0-dev","modified":false,"go_version":"go1.26"}`},
	} {
		got, err := json.Marshal(c.info)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestSigningKey(t *testing.T) {
	key := SigningKey()
	if !bytes.HasPrefix(key, []byte("untrusted comment: minisign public key ")) {
		t.Fatalf("not a minisign .pub file: %q", key)
	}
	key[0] = 'X'
	if got := SigningKey(); got[0] == 'X' {
		t.Error("SigningKey returns the embedded bytes themselves, so a caller can change the key")
	}
}
