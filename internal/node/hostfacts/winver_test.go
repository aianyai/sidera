package hostfacts

import "testing"

func TestWindowsLabel(t *testing.T) {
	tests := []struct {
		name                string
		major, minor, build uint32
		displayVersion      string
		want                string
	}{
		{"build 19045 is Windows 10", 10, 0, 19045, "", "Windows 10"},
		{"build 21999 is still Windows 10", 10, 0, 21999, "", "Windows 10"},
		{"build 22000 is Windows 11", 10, 0, 22000, "", "Windows 11"},
		{"build 26100 is Windows 11", 10, 0, 26100, "", "Windows 11"},
		{"the display version follows the product", 10, 0, 26100, "24H2", "Windows 11 24H2"},
		{"the display version of Windows 10", 10, 0, 19045, "22H2", "Windows 10 22H2"},
		{"a display version is trimmed", 10, 0, 26100, " 24H2 ", "Windows 11 24H2"},
		{"an older major names its version", 6, 3, 9600, "", "Windows 6.3"},
		{"an older major takes no Windows 11 shortcut", 6, 1, 22000, "", "Windows 6.1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsLabel(tc.major, tc.minor, tc.build, tc.displayVersion); got != tc.want {
				t.Fatalf("windowsLabel(%d, %d, %d, %q) = %q, want %q", tc.major, tc.minor, tc.build, tc.displayVersion, got, tc.want)
			}
		})
	}
}

func TestWindowsBuild(t *testing.T) {
	zero, ubr := uint32(0), uint32(4061)
	tests := []struct {
		name                string
		major, minor, build uint32
		ubr                 *uint32
		want                string
	}{
		{"without a revision the build ends at the build number", 10, 0, 26100, nil, "10.0.26100"},
		{"a revision is appended", 10, 0, 26100, &ubr, "10.0.26100.4061"},
		{"a revision of zero is still appended", 10, 0, 19045, &zero, "10.0.19045.0"},
		{"an older major keeps its own numbers", 6, 3, 9600, nil, "6.3.9600"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsBuild(tc.major, tc.minor, tc.build, tc.ubr); got != tc.want {
				t.Fatalf("windowsBuild(%d, %d, %d, %v) = %q, want %q", tc.major, tc.minor, tc.build, tc.ubr, got, tc.want)
			}
		})
	}
}
