//go:build unix

package fsx_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// A symbolic link among the files CheckPrivateTree judges is accepted only when it
// resolves to a socket that its owner alone can reach, in a directory that its
// owner alone can reach: it is how a runtime whose socket path is too long for the
// system keeps the path it was given. Every other link is refused, and a refusal
// of a socket names both the link and the socket.
func TestCheckPrivateTreeJudgesALinkByItsTarget(t *testing.T) {
	const link = "state/app-server.sock"
	for _, tc := range []struct {
		name   string
		target func(t *testing.T, dir string) string // makes the target in dir, returns its name
		dirMod os.FileMode                           // mode of the directory that holds the target
		mode   os.FileMode                           // mode of the target
		refuse bool
		// detail is what the refusal must name beyond the link: the target, or the
		// directory that holds it.
		detail func(targetDir, target string) string
	}{
		{"a socket in a private directory", socket, 0o700, 0o600, false, nil},
		{"a socket the group can reach", socket, 0o700, 0o660, true, func(_, target string) string { return target }},
		{"a socket others can reach", socket, 0o700, 0o606, true, func(_, target string) string { return target }},
		{"a socket in a directory others can reach", socket, 0o755, 0o600, true, func(dir, _ string) string { return dir }},
		{"a regular file", regularFile, 0o700, 0o600, true, nil},
		{"a directory", directory, 0o700, 0o700, true, nil},
		{"nothing", nothing, 0o700, 0o600, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := os.MkdirTemp("", "sx")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(base) })
			// The target is outside the tree, as the short directory a runtime links
			// to is, and its path stays under the limit of a socket path.
			root, targetDir := filepath.Join(base, "r"), filepath.Join(base, "t")
			for _, dir := range []string{root, filepath.Join(root, "state"), targetDir} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			name := tc.target(t, targetDir)
			if err := os.Chmod(filepath.Join(targetDir, name), tc.mode); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := os.Chmod(targetDir, tc.dirMod); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(targetDir, name), filepath.Join(root, link)); err != nil {
				t.Fatal(err)
			}
			r, err := fsx.OpenOSReader(root)
			if err != nil {
				t.Fatal(err)
			}
			err = fsx.CheckPrivateTree(r, ".", func(rel string) bool { return strings.HasPrefix(rel, "state/") })
			if !tc.refuse {
				if err != nil {
					t.Fatalf("CheckPrivateTree = %v, want nil", err)
				}
				return
			}
			var refusal *fsx.NotPrivateError
			if !errors.As(err, &refusal) || refusal.Path != link {
				t.Fatalf("CheckPrivateTree = %#v, want a *NotPrivateError for %s", err, link)
			}
			if tc.detail != nil {
				resolvedDir, err := filepath.EvalSymlinks(targetDir)
				if err != nil {
					t.Fatal(err)
				}
				if want := tc.detail(resolvedDir, filepath.Join(resolvedDir, name)); !strings.Contains(refusal.Detail, want) {
					t.Errorf("Detail = %q, want it to name %s", refusal.Detail, want)
				}
			}
		})
	}
}

// socket makes a listening Unix socket, closed with the test.
func socket(t *testing.T, dir string) string {
	t.Helper()
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return "s"
}

func regularFile(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return "f"
}

func directory(t *testing.T, dir string) string {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	return "d"
}

func nothing(*testing.T, string) string { return "gone" }
