package launch

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// program writes an executable file named like the program and returns its path.
func program(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("program"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCaptureRecordsTheProgramAsStarted(t *testing.T) {
	start := t.TempDir()
	real := program(t, filepath.Join(start, "cellar"), "sidera")
	other := program(t, filepath.Join(start, "other"), "sidera")
	link := filepath.Join(start, "bin", "sidera")
	if runtime.GOOS == "windows" {
		link += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symbolic links are not available: %v", err)
	}
	exe := func(path string, err error) func() (string, error) {
		return func() (string, error) { return path, err }
	}
	sep := string(os.PathSeparator)
	onPath := t.TempDir()
	onPathProgram := program(t, onPath, "sidera")

	for _, tc := range []struct {
		name       string
		arg0       string
		path       string // PATH during the case
		executable func() (string, error)
		want       string
		wantErr    bool
	}{
		{"absolute path", real, "", exe(real, nil), real, false},
		{"relative path is taken from the start directory", "." + sep + filepath.Join("cellar", filepath.Base(real)), "", exe(real, nil), real, false},
		{"bare name is looked up in PATH", "sidera", onPath, exe(onPathProgram, nil), onPathProgram, false},
		{"a symbolic link is kept", link, "", exe(real, nil), link, false},
		{"a link found through PATH is kept", "sidera", filepath.Dir(link), exe(real, nil), link, false},
		{"another file than argv[0] names: the executable", real, "", exe(other, nil), other, false},
		{"argv[0] cannot be found: the executable", "no-such-sidera", t.TempDir(), exe(real, nil), real, false},
		{"the executable is unknown: argv[0]", real, "", exe("", errors.New("unknown")), real, false},
		{"neither can be found", "no-such-sidera", t.TempDir(), exe("", errors.New("unknown")), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", tc.path)
			argv := []string{tc.arg0, "daemon", "--home", "relative-home"}
			got, err := capture(argv, start, tc.executable)
			if (err != nil) != tc.wantErr {
				t.Fatalf("capture error = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got.Program != tc.want {
				t.Errorf("Program = %q, want %q", got.Program, tc.want)
			}
			if got.Dir != start {
				t.Errorf("Dir = %q, want %q", got.Dir, start)
			}
			if len(got.Args) != len(argv) || got.Args[0] != tc.arg0 || got.Args[3] != "relative-home" {
				t.Errorf("Args = %q, want %q", got.Args, argv)
			}
			argv[1] = "changed"
			if got.Args[1] != "daemon" {
				t.Error("Args shares its memory with the argv it was captured from")
			}
		})
	}
}
