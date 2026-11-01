package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/keys"
)

// The permissions of the data directory and of keys/ are judged before anything
// else is done, and a start that does not like them stops: it does not narrow
// them, does not take the lock, does not make a socket. Only the owner knows who
// ought to read the keys. The checks fail closed: a mode that any other user can
// reach, and a data directory that is not a directory (a symbolic link is one
// that is not), are refusals.

func chmod(t testing.TB, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func TestStartRefusesADataDirectoryThatOthersCanReach(t *testing.T) {
	requireUnix(t)
	for _, mode := range []os.FileMode{0o755, 0o750, 0o705, 0o770, 0o777, 0o701} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			c, _ := initialized(t)
			chmod(t, c.Home, mode)
			before := snapshot(t, c.Home)
			_, err := daemon.Start(context.Background(), c)
			if !errors.Is(err, daemon.ErrPermissions) {
				t.Fatalf("a start on a data directory of mode %04o: %v; want ErrPermissions", mode, err)
			}
			if !strings.Contains(err.Error(), c.Home) {
				t.Errorf("the refusal %q does not name the directory", err)
			}
			if after := snapshot(t, c.Home); !reflect.DeepEqual(before, after) {
				t.Errorf("a refused start changed the data directory:\n%s", diff(before, after))
			}
			if _, err := os.Lstat(filepath.Join(c.Home, "manage", "manage.sock")); err == nil {
				t.Error("a socket was made")
			}
		})
	}
}

// init does not narrow the mode of a directory that already exists either: the
// owner decides what becomes of it.
func TestInitRefusesAnExistingDataDirectoryThatOthersCanReach(t *testing.T) {
	requireUnix(t)
	home := newHome(t)
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	chmod(t, home, 0o755)
	_, err := daemon.Init(context.Background(), config(home, clocktest.NewFake(t0)))
	if !errors.Is(err, daemon.ErrPermissions) {
		t.Fatalf("init on a directory of mode 0755: %v; want ErrPermissions", err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("a refused init left %d entries", len(entries))
	}
	if info, _ := os.Stat(home); info.Mode().Perm() != 0o755 {
		t.Errorf("the mode was changed to %v", info.Mode().Perm())
	}
}

// A data directory that is a symbolic link is refused, whatever it points to and
// whoever owns the target: a link can be replaced between the check and the use,
// and what it points to is not what the owner made with init. A link above the
// data directory is another matter and is followed: the system's own temporary
// directory is one on macOS.
func TestStartAndInitRefuseASymbolicLinkAsTheDataDirectory(t *testing.T) {
	requireUnix(t)
	c, clk := initialized(t)
	link := newHome(t) // a path that does not exist, in a short directory
	if err := os.Symlink(c.Home, link); err != nil {
		t.Fatal(err)
	}
	lc := config(link, clk)
	before := snapshot(t, c.Home)
	if _, err := daemon.Start(context.Background(), lc); !errors.Is(err, daemon.ErrPermissions) {
		t.Errorf("a start on a symbolic link: %v; want ErrPermissions", err)
	}
	if _, err := daemon.Init(context.Background(), lc); !errors.Is(err, daemon.ErrPermissions) {
		t.Errorf("init on a symbolic link: %v; want ErrPermissions", err)
	}
	if after := snapshot(t, c.Home); !reflect.DeepEqual(before, after) {
		t.Errorf("a refused start or init changed what the link points to:\n%s", diff(before, after))
	}
	// the directory itself still starts
	started(t, c)
}

func TestStartRefusesKeysThatOthersCanReach(t *testing.T) {
	requireUnix(t)
	for _, tc := range []struct {
		name string
		path string
		mode os.FileMode
	}{
		{"the directory", "keys", 0o755},
		{"the directory, for the group", "keys", 0o750},
		{"the identity key", "keys/identity.key", 0o644},
		{"the network key", "keys/network.key", 0o640},
		{"the self-revocation", "keys/self-revocation.rec", 0o604},
		{"the clock record", "keys/clock.rec", 0o660},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := initialized(t)
			p := filepath.Join(c.Home, tc.path)
			chmod(t, p, tc.mode)
			before := snapshot(t, c.Home)
			_, err := daemon.Start(context.Background(), c)
			if !errors.Is(err, keys.ErrPermissionsTooBroad) {
				t.Fatalf("a start with %s of mode %04o: %v; want keys.ErrPermissionsTooBroad", tc.path, tc.mode, err)
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Errorf("the refusal %q does not name %s", err, tc.path)
			}
			if after := snapshot(t, c.Home); !reflect.DeepEqual(before, after) {
				t.Errorf("a refused start changed the data directory:\n%s", diff(before, after))
			}
		})
	}
}

// A key that is a symbolic link is refused whatever it points to.
func TestStartRefusesAKeyThatIsASymbolicLink(t *testing.T) {
	requireUnix(t)
	c, _ := initialized(t)
	key := filepath.Join(c.Home, "keys", "network.key")
	other := filepath.Join(c.Home, "keys", "elsewhere")
	if err := os.Rename(key, other); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, key); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, c.Home)
	_, err := daemon.Start(context.Background(), c)
	if !errors.Is(err, keys.ErrPermissionsTooBroad) {
		t.Fatalf("a start with a key that is a symbolic link: %v; want keys.ErrPermissionsTooBroad", err)
	}
	if after := snapshot(t, c.Home); !reflect.DeepEqual(before, after) {
		t.Errorf("a refused start changed the data directory:\n%s", diff(before, after))
	}
}

// The checks come before the lock: a data directory whose permissions are refused
// has no lock file made for it, and the refusal does not depend on whether
// another daemon runs.
func TestThePermissionsAreJudgedBeforeTheLockIsTaken(t *testing.T) {
	requireUnix(t)
	c, _ := initialized(t)
	if err := os.Remove(filepath.Join(c.Home, "daemon.lock")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	chmod(t, c.Home, 0o755)
	if _, err := daemon.Start(context.Background(), c); !errors.Is(err, daemon.ErrPermissions) {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "daemon.lock")); err == nil {
		t.Error("a lock file was made on a directory whose permissions were refused")
	}
}

// treeFixture puts, in an initialized data directory, the places that matter
// besides the root and keys/: the token of a session endpoint, an adapter's state
// directory (protected on its own, as the host makes it) with the token file in
// it, and a record file that holds nothing secret.
func treeFixture(t testing.TB, home string) {
	t.Helper()
	for _, dir := range []string{"adapter-state", "adapter-state/codex"} {
		if err := fsx.MakeRoot(filepath.Join(home, dir)); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"state/endpoints/e.rec", "adapter-state/codex/ws-token", "state/in/m.rec"} {
		if err := os.WriteFile(filepath.Join(home, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A start judges the whole tree, not the root alone: a directory made protected
// on its own, as adapter-state/<instance> is, does not follow the root, and the
// file that holds a token is judged by itself. The refusal names the path. A
// record file that holds no secret relies on its directory and is not judged.
// widen lets someone other than the owner reach a path, as the platform does it.
func TestStartRefusesAPrivateTreeThatOthersCanReach(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		refused error // what the start returns, nil when it starts
	}{
		{"a directory of the state", "state/in", daemon.ErrPermissions},
		{"a state directory of an instance", "adapter-state/codex", daemon.ErrPermissions},
		{"the directory of the adapters' state", "adapter-state", daemon.ErrPermissions},
		{"the token of an endpoint", "state/endpoints/e.rec", daemon.ErrPermissions},
		{"the token file of an adapter", "adapter-state/codex/ws-token", daemon.ErrPermissions},
		{"a key", "keys/identity.key", keys.ErrPermissionsTooBroad},
		{"a record that holds no secret", "state/in/m.rec", nil},
		{"a clean tree", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := initialized(t)
			treeFixture(t, c.Home)
			p := filepath.Join(c.Home, filepath.FromSlash(tc.path))
			if tc.path != "" {
				widen(t, p)
			}
			if tc.refused == nil {
				started(t, c)
				return
			}
			before := snapshot(t, c.Home)
			_, err := daemon.Start(context.Background(), c)
			if !errors.Is(err, tc.refused) {
				t.Fatalf("a start with %s widened: %v; want %v", tc.path, err, tc.refused)
			}
			if !strings.Contains(err.Error(), tc.path) && !strings.Contains(err.Error(), p) {
				t.Errorf("the refusal %q does not name %s", err, p)
			}
			if after := snapshot(t, c.Home); !reflect.DeepEqual(before, after) {
				t.Errorf("a refused start changed the data directory:\n%s", diff(before, after))
			}
		})
	}
}
