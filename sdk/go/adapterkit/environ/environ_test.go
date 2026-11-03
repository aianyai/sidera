package environ

import (
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestCompose(t *testing.T) {
	sep := string(filepath.ListSeparator)
	dropUpper := func(name string) bool { return name == "DROP" || name == "KEEP" }

	tests := []struct {
		name string
		fold bool
		base []string
		spec Spec
		want []string
	}{
		{
			name: "invalid pairs are left out",
			base: []string{"A=1", "BAD=\xff", "\xffBAD=x", "NOEQUALS", "=x", "", "B="},
			want: []string{"A=1", "B="},
		},
		{
			name: "a leading equals sign is an empty name elsewhere",
			base: []string{"=C:=C:\\dir", "A=1"},
			want: []string{"A=1"},
		},
		{
			name: "a leading equals sign belongs to the name on Windows",
			fold: true,
			base: []string{"=C:=C:\\dir", "=D:", "A=1"},
			want: []string{"=C:=C:\\dir", "A=1"},
		},
		{
			name: "a repeated name keeps its last value at its last place",
			base: []string{"A=1", "B=1", "A=2"},
			want: []string{"B=1", "A=2"},
		},
		{
			name: "names differing in case are two names elsewhere",
			base: []string{"Path=1", "PATH=2"},
			want: []string{"Path=1", "PATH=2"},
		},
		{
			name: "names differing in case are one name on Windows",
			fold: true,
			base: []string{"Path=1", "PATH=2"},
			want: []string{"PATH=2"},
		},
		{
			name: "drop sees the name as written elsewhere",
			base: []string{"drop=1", "DROP=2", "A=1"},
			spec: Spec{Drop: dropUpper},
			want: []string{"drop=1", "A=1"},
		},
		{
			name: "drop sees the name in upper case on Windows",
			fold: true,
			base: []string{"drop=1", "A=1"},
			spec: Spec{Drop: dropUpper},
			want: []string{"A=1"},
		},
		{
			name: "set replaces in place and appends in name order",
			base: []string{"A=1", "B=1", "C=1"},
			spec: Spec{Set: map[string]string{"B": "2", "Z": "2", "N": "2"}},
			want: []string{"A=1", "B=2", "C=1", "N=2", "Z=2"},
		},
		{
			name: "set matches a name without case on Windows",
			fold: true,
			base: []string{"a=1", "B=1"},
			spec: Spec{Set: map[string]string{"A": "2"}},
			want: []string{"A=2", "B=1"},
		},
		{
			name: "a variable set is never dropped",
			base: []string{"KEEP=1", "DROP=1"},
			spec: Spec{Drop: dropUpper, Set: map[string]string{"KEEP": "2", "NEW": "3"}},
			want: []string{"KEEP=2", "NEW=3"},
		},
		{
			name: "the path prefix goes in front of PATH",
			base: []string{"A=1", "PATH=/usr/bin"},
			spec: Spec{PathPrefix: "/opt"},
			want: []string{"A=1", "PATH=/opt" + sep + "/usr/bin"},
		},
		{
			name: "the path prefix becomes PATH when it is missing",
			base: []string{"A=1"},
			spec: Spec{PathPrefix: "/opt"},
			want: []string{"A=1", "PATH=/opt"},
		},
		{
			name: "the path prefix becomes PATH when it is empty",
			base: []string{"PATH=", "A=1"},
			spec: Spec{PathPrefix: "/opt"},
			want: []string{"PATH=/opt", "A=1"},
		},
		{
			name: "the path prefix is put in front of a PATH that set gives",
			base: []string{"PATH=/usr/bin"},
			spec: Spec{PathPrefix: "/opt", Set: map[string]string{"PATH": "/set"}},
			want: []string{"PATH=/opt" + sep + "/set"},
		},
		{
			name: "another spelling is not PATH elsewhere",
			base: []string{"Path=/usr/bin"},
			spec: Spec{PathPrefix: "/opt"},
			want: []string{"Path=/usr/bin", "PATH=/opt"},
		},
		{
			name: "the path prefix keeps the spelling the environment uses on Windows",
			fold: true,
			base: []string{"Path=/usr/bin"},
			spec: Spec{PathPrefix: "/opt"},
			want: []string{"Path=/opt" + sep + "/usr/bin"},
		},
		{
			name: "an empty environment stays empty",
			want: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := slices.Clone(tt.base)
			got := compose(tt.base, tt.spec, tt.fold)
			if got == nil || !slices.Equal(got, tt.want) {
				t.Fatalf("compose = %q, want %q", got, tt.want)
			}
			if !slices.Equal(tt.base, before) {
				t.Fatalf("compose changed its input: %q, was %q", tt.base, before)
			}
		})
	}
}

// TestComposeFoldsCaseOnWindowsOnly guards the choice of the case rule that
// Compose makes for the platform.
func TestComposeFoldsCaseOnWindowsOnly(t *testing.T) {
	got := Compose([]string{"Path=1", "PATH=2"}, Spec{})
	if folded := len(got) == 1; folded != (runtime.GOOS == "windows") {
		t.Fatalf("Compose(Path, PATH) = %q on %s", got, runtime.GOOS)
	}
}
