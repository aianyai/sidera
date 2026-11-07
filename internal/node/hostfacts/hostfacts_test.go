package hostfacts

import "testing"

func TestFirstLabel(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"a dotted name keeps its first label", "imac.local", "imac"},
		{"a fully qualified name keeps its first label", "build-box.example.com", "build-box"},
		{"a bare name is unchanged", "inuc", "inuc"},
		{"case is kept", "vmWin11", "vmWin11"},
		{"surrounding space is trimmed", "  imac.local\n", "imac"},
		{"an empty name stays empty", "", ""},
		{"a leading dot leaves no label", ".local", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstLabel(tc.in); got != tc.want {
				t.Fatalf("firstLabel(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
