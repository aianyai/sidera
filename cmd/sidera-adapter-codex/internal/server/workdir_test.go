package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-codex/internal/appserver"
)

// cwdFileEnv names the file a stand-in for Codex writes its working directory to.
// The stand-in is this test binary: TestMain sees the variable and acts as it.
const cwdFileEnv = "SIDERA_TEST_FAKE_CODEX_CWD_FILE"

// hangFileEnv makes the stand-in write its PID to the named file and stay, never
// answering.
const hangFileEnv = "SIDERA_TEST_FAKE_CODEX_HANG_FILE"

func TestMain(m *testing.M) {
	if path := os.Getenv(hangFileEnv); path != "" {
		_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
		time.Sleep(time.Minute)
		os.Exit(1)
	}
	if path := os.Getenv(cwdFileEnv); path != "" {
		wd, err := os.Getwd()
		if err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(path, []byte(wd), 0o600)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// The server runs in an empty directory of the adapter's own under the user's
// cache directory, never in the state directory, which holds the WebSocket token:
// Codex makes the server's working directory a writable root of sandboxed
// commands. A restart of the same instance finds the same directory; another
// instance, or the same instance of another Sidera home, gets its own. The start
// records the directory, so that a later adapter can tell this server from one
// that ran in the state directory.
func TestStartRunsTheServerInADirectoryOfItsOwn(t *testing.T) {
	// Resolved, because the stand-in reports its directory with symbolic links
	// resolved (macOS's temporary directory is behind one).
	cache, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := func(stateDir string) (cwd string, st state) {
		t.Helper()
		cwdFile := filepath.Join(t.TempDir(), "cwd")
		srv := New(Config{
			Program:      os.Args[0],
			StateDir:     stateDir,
			Environ:      append(os.Environ(), cwdFileEnv+"="+cwdFile),
			StartTimeout: 10 * time.Second,
		})
		srv.userCacheDir = func() (string, error) { return cache, nil }
		srv.probe = func(context.Context, appserver.Endpoint) error { return errors.New("not up") }
		// The stand-in exits at once, so the start fails; it has run by then.
		if _, err := srv.Connect(context.Background()); !errors.Is(err, ErrStartFailed) {
			t.Fatalf("Connect: %v, want the stand-in to have exited", err)
		}
		raw, err := os.ReadFile(cwdFile)
		if err != nil {
			t.Fatalf("the stand-in did not run: %v", err)
		}
		st, err = srv.loadState()
		if err != nil {
			t.Fatal(err)
		}
		return string(raw), st
	}
	// A socket path must be short, so the state directories are too.
	home := shortTempDir(t)
	first := filepath.Join(home, "adapter-state", "codex")
	second := filepath.Join(home, "adapter-state", "codex-2")
	otherHome := filepath.Join(shortTempDir(t), "adapter-state", "codex")
	for _, d := range []string{first, second, otherHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	cwd, st := start(first)
	want := filepath.Join(cache, workDirParent)
	if filepath.Dir(cwd) != want || !strings.HasPrefix(filepath.Base(cwd), "codex-") {
		t.Fatalf("the server ran in %s, want a directory of the instance under %s", cwd, want)
	}
	if st.WorkDir != cwd {
		t.Errorf("state file records %q, the server ran in %q", st.WorkDir, cwd)
	}
	if st.PID <= 0 || st.StartKey == "" || st.Container.Validate() != nil {
		t.Errorf("state file records pid %d, start key %q and container %+v: the process must be identifiable and its container valid", st.PID, st.StartKey, st.Container)
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Errorf("the server's directory holds %v (%v), want nothing", entries, err)
	}
	if again, _ := start(first); again != cwd {
		t.Errorf("a restart ran the server in %s, then in %s", cwd, again)
	}
	for _, other := range []string{second, otherHome} {
		if got, _ := start(other); got == cwd {
			t.Errorf("the server of %s ran in %s, the directory of %s", other, got, first)
		}
	}
}

// A server that answers but was not started by this process is reported as
// exposed when its record says it ran in the state directory: an earlier adapter
// started every server there and recorded no working directory.
func TestConnectReportsAServerThatRunsInTheStateDirectory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   *state
		exposed bool
	}{
		{"recorded working directory", &state{PID: 7, Address: "a", WorkDir: "/cache/sidera-adapter-codex/codex-0"}, false},
		{"no working directory recorded", &state{PID: 7, Address: "a"}, true},
		{"no state file", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t)
			if tc.state != nil {
				raw, _ := json.Marshal(tc.state)
				if err := os.WriteFile(filepath.Join(dir, stateFileName), raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv := New(Config{Program: "codex", StateDir: dir})
			srv.goos = "linux"
			srv.probe = func(context.Context, appserver.Endpoint) error { return nil }
			if _, err := srv.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := srv.Exposure() != ""; got != tc.exposed {
				t.Fatalf("exposure = %q, want exposed %v", srv.Exposure(), tc.exposed)
			}
		})
	}
}

// shortTempDir is a temporary directory short enough to hold a socket path.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
