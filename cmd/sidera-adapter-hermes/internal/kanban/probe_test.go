package kanban

import (
	"strings"
	"testing"
)

func TestParseAndCheckVersion(t *testing.T) {
	for _, tc := range []struct {
		output  string
		version string // "" when parsing must fail
		ok      bool   // inside the supported range
	}{
		{"Hermes Agent v0.22.2+custom (2026.9.24) · stable · local 0123456789ab\n", "0.22.2", true},
		{"Hermes Agent v0.22.2\r\nInstall directory: C:\\hermes\r\n", "0.22.2", true},
		{"Hermes Agent v0.22.7 (2026.11.3)\n", "0.22.7", true},
		{"Hermes Agent v0.22.1+custom\n", "0.22.1", false},
		{"Hermes Agent v0.23.0\n", "0.23.0", false},
		{"Hermes Agent v0.23.0-rc1\n", "0.23.0", false},
		{"Hermes Agent v1.0.0\n", "1.0.0", false},
		{"hermes 0.22.2\n", "", false},
		{"", "", false},
	} {
		v, err := ParseVersion(tc.output)
		if tc.version == "" {
			if err == nil {
				t.Errorf("%q: parsed as %v", tc.output, v)
			}
			continue
		}
		if err != nil || v.String() != tc.version {
			t.Errorf("%q: version %v err %v, want %s", tc.output, v, err, tc.version)
			continue
		}
		err = CheckVersion(v)
		if (err == nil) != tc.ok {
			t.Errorf("%s: CheckVersion = %v, want ok=%v", tc.version, err, tc.ok)
		}
		if err != nil && (!strings.Contains(err.Error(), tc.version) ||
			!strings.Contains(err.Error(), SupportedFrom) || !strings.Contains(err.Error(), SupportedBefore)) {
			t.Errorf("%s: error %q does not name the version and the range", tc.version, err)
		}
	}
	out := string(sample(t, "version.txt"))
	if v, err := ParseVersion(out); err != nil || CheckVersion(v) != nil {
		t.Errorf("recorded version output %q: %v %v", out, v, err)
	}
}

func TestCheckExecutable(t *testing.T) {
	for _, tc := range []struct {
		goos, path string
		refused    bool
	}{
		{"windows", `C:\Users\u\AppData\Local\hermes\hermes.cmd`, true},
		{"windows", `C:\tools\HERMES.BAT`, true},
		{"windows", `C:\Users\u\AppData\Local\hermes\hermes.exe`, false},
		{"windows", `C:\tools\hermes`, false},
		{"linux", "/usr/local/bin/hermes.cmd", false},
		{"darwin", "/usr/local/bin/hermes", false},
	} {
		if err := CheckExecutable(tc.goos, tc.path); (err != nil) != tc.refused {
			t.Errorf("%s %s: err = %v, want refused=%v", tc.goos, tc.path, err, tc.refused)
		}
	}
}

func TestCheckProfiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		as      []Assignee
		refused bool
	}{
		{"ordinary profiles", []Assignee{{"default", true}, {"work", true}}, false},
		{"lane assignees on cards only", []Assignee{{"sidera", false}, {"sidera:node-b", false}, {"sidera-inbox", false}}, false},
		{"profile sidera", []Assignee{{"default", true}, {"sidera", true}}, true},
		{"profile sidera-inbox", []Assignee{{"sidera-inbox", true}}, true},
		{"profile sidera:alias", []Assignee{{"sidera:node-b", true}}, true},
		{"similar name", []Assignee{{"sidera-extra", true}, {"sideral", true}}, false},
	} {
		if err := CheckProfiles(tc.as); (err != nil) != tc.refused {
			t.Errorf("%s: err = %v, want refused=%v", tc.name, err, tc.refused)
		}
	}
}
