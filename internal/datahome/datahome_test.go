package datahome_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aianyai/sidera/internal/datahome"
)

// host is a machine with the given environment and directories.
func host(goos string, env map[string]string, dirs ...string) datahome.Host {
	files := fstest.MapFS{}
	for _, d := range dirs {
		files[filepath.ToSlash(d)[1:]] = &fstest.MapFile{Mode: fs.ModeDir}
	}
	return datahome.Host{
		OS:          goos,
		Getenv:      func(k string) string { return env[k] },
		UserHomeDir: func() (string, error) { return "/users/ann", nil },
		Lstat: func(p string) (fs.FileInfo, error) {
			info, err := fs.Stat(files, filepath.ToSlash(p)[1:])
			if err != nil {
				return nil, &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
			}
			return info, nil
		},
	}
}

func TestResolve(t *testing.T) {
	local := map[string]string{"LOCALAPPDATA": "/users/ann/appdata/local"}
	for _, tc := range []struct {
		name     string
		explicit string
		env      map[string]string
		goos     string
		dirs     []string
		want     string
	}{
		{"unix default", "", nil, "linux", nil, "/users/ann/.sidera"},
		{"macOS default ignores a former directory rule", "", nil, "darwin", []string{"/users/ann/.sidera"}, "/users/ann/.sidera"},
		{"windows default is under the local application data", "", local, "windows", nil, "/users/ann/appdata/local/Sidera"},
		{"windows default stays when it exists beside the former one", "", local, "windows",
			[]string{"/users/ann/appdata/local/Sidera", "/users/ann/.sidera"}, "/users/ann/appdata/local/Sidera"},
		{"the flag wins on every platform", "/data/flag", map[string]string{"SIDERA_HOME": "/data/env"}, "linux", nil, "/data/flag"},
		{"the environment wins over the default", "", map[string]string{"SIDERA_HOME": "/data/env"}, "darwin", nil, "/data/env"},
		{"the flag is not checked against the former directory", "/data/flag", local, "windows", []string{"/users/ann/.sidera"}, "/data/flag"},
		{"the environment is not checked against the former directory", "",
			map[string]string{"SIDERA_HOME": "/data/env", "LOCALAPPDATA": "/users/ann/appdata/local"}, "windows", []string{"/users/ann/.sidera"}, "/data/env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, _ := filepath.Abs(filepath.FromSlash(tc.want)) // a drive is added on Windows
			got, err := datahome.Resolve(tc.explicit, host(tc.goos, tc.env, tc.dirs...))
			if err != nil || got != want {
				t.Fatalf("Resolve = %q, %v; want %q", got, err, want)
			}
		})
	}
}

// A Windows machine that has the former default and not the new one is refused
// with the way to move it, and nothing is resolved.
func TestResolveRefusesTheFormerWindowsDirectory(t *testing.T) {
	env := map[string]string{"LOCALAPPDATA": "/users/ann/appdata/local"}
	got, err := datahome.Resolve("", host("windows", env, "/users/ann/.sidera"))
	var moved *datahome.MovedError
	if !errors.As(err, &moved) || got != "" {
		t.Fatalf("Resolve = %q, %v; want a *MovedError and no directory", got, err)
	}
	for _, want := range []string{"Move-Item", "SIDERA_HOME", moved.Old, moved.New} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message %q does not say %q", err, want)
		}
	}
}

func TestResolveWindowsNeedsTheLocalApplicationData(t *testing.T) {
	if _, err := datahome.Resolve("", host("windows", nil)); err == nil {
		t.Fatal("Resolve without LOCALAPPDATA succeeded")
	}
}

// Looking at a directory that fails for another reason than its absence is not
// read as its absence: the check is a guard against starting an empty node.
func TestResolveReportsAFailureToLookAtTheDirectories(t *testing.T) {
	h := host("windows", map[string]string{"LOCALAPPDATA": "/x"})
	h.Lstat = func(p string) (fs.FileInfo, error) { return nil, os.ErrPermission }
	if _, err := datahome.Resolve("", h); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Resolve = %v, want the permission error", err)
	}
}
