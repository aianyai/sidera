//go:build windows

package realpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func resolved(t *testing.T) string {
	t.Helper()
	dir, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveFollowsAJunction(t *testing.T) {
	dir := resolved(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(dir, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v: %s", err, out)
	}
	got, err := Resolve(filepath.Join(junction, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(target, "file"); got != want {
		t.Fatalf("Resolve through a junction = %q, want %q", got, want)
	}
}

func TestResolveFollowsADirectorySymlink(t *testing.T) {
	dir := resolved(t)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	got, err := Resolve(link)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("Resolve through a symbolic link = %q, want %q", got, target)
	}
}

func TestResolveReturnsTheOnDiskCase(t *testing.T) {
	dir := resolved(t)
	want := filepath.Join(dir, "MixedDir", "MixedFile.txt")
	if err := os.Mkdir(filepath.Join(dir, "MixedDir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(strings.ToLower(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Resolve of a lower-case spelling = %q, want %q", got, want)
	}
}

func TestResolveReturnsTheLongForm(t *testing.T) {
	dir := resolved(t)
	long := filepath.Join(dir, "A Directory With A Long Name")
	if err := os.Mkdir(long, 0o700); err != nil {
		t.Fatal(err)
	}
	from, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_PATH)
	n, err := windows.GetShortPathName(from, &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	short := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(short, long) {
		t.Skip("this volume does not create 8.3 names")
	}
	got, err := Resolve(short)
	if err != nil {
		t.Fatal(err)
	}
	if got != long {
		t.Fatalf("Resolve(%q) = %q, want %q", short, got, long)
	}
}

func TestTrimExtendedPrefix(t *testing.T) {
	tests := []struct {
		in, want string
		bad      bool
	}{
		{in: `\\?\C:\dir\file`, want: `C:\dir\file`},
		{in: `\\?\C:\`, want: `C:\`},
		{in: `\\?\UNC\srv\share\dir`, want: `\\srv\share\dir`},
		{in: `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\dir`, bad: true},
		{in: `C:\dir`, bad: true},
	}
	for _, tt := range tests {
		got, err := trimExtendedPrefix(tt.in)
		if tt.bad {
			if err == nil {
				t.Errorf("trimExtendedPrefix(%q) = %q, want an error", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("trimExtendedPrefix(%q) = %q, %v, want %q", tt.in, got, err, tt.want)
		}
	}
}
