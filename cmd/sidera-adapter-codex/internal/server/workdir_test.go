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

// cwdFileEnv names the file a stand-in for Codex writes its working directory to,
// and then stays, never answering. The stand-in is this test binary: TestMain sees
// the variable and acts as it.
const cwdFileEnv = "SIDERA_TEST_FAKE_CODEX_CWD_FILE"

// exitEnv makes the stand-in exit at once, as a server does that lost the socket to
// an earlier one.
const exitEnv = "SIDERA_TEST_FAKE_CODEX_EXIT"

// hangFileEnv makes the stand-in write its PID to the named file and stay, never
// answering.
const hangFileEnv = "SIDERA_TEST_FAKE_CODEX_HANG_FILE"

func TestMain(m *testing.M) {
	if os.Getenv(exitEnv) != "" {
		os.Exit(1)
	}
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
		time.Sleep(time.Minute)
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
			StartTimeout: 500 * time.Millisecond,
		})
		srv.userCacheDir = func() (string, error) { return cache, nil }
		// The record is read at the first probe, while the start waits for the stand-in.
		var loadErr error
		srv.probe = func(context.Context, appserver.Endpoint) error {
			if st.PID == 0 {
				st, loadErr = srv.loadState()
			}
			return errors.New("not up")
		}
		// The stand-in never answers, so the start fails and ends it; it has run by then.
		if _, err := srv.Connect(context.Background()); !errors.Is(err, ErrStartFailed) {
			t.Fatalf("Connect: %v, want the start to fail for want of an answer", err)
		}
		raw, err := os.ReadFile(cwdFile)
		if err != nil {
			t.Fatalf("the stand-in did not run: %v", err)
		}
		if loadErr != nil {
			t.Fatal(loadErr)
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

// Deleting the working directory takes the instance's own and nothing else: not the
// directory of another instance, and not the cache directory of the adapter that
// holds them. Deleting one that is not there is not an error.
func TestRemoveWorkDirDeletesOnlyTheInstancesOwn(t *testing.T) {
	cache := t.TempDir()
	home := shortTempDir(t)
	newServer := func(name string) *Server {
		srv := New(Config{Program: "codex", StateDir: filepath.Join(home, "adapter-state", name)})
		srv.userCacheDir = func() (string, error) { return cache, nil }
		return srv
	}
	mine, other := newServer("codex"), newServer("codex-2")
	var dirs [2]string
	for i, srv := range []*Server{mine, other} {
		dir, err := srv.workDir()
		if err != nil {
			t.Fatal(err)
		}
		dirs[i] = dir
	}

	for range 2 {
		if err := mine.RemoveWorkDir(); err != nil {
			t.Fatalf("RemoveWorkDir: %v", err)
		}
	}
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Errorf("the instance's directory is still there (stat: %v)", err)
	}
	if _, err := os.Stat(dirs[1]); err != nil {
		t.Errorf("the directory of another instance: %v", err)
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

// Stop, retire and replace find a server only through the state file. A start
// writes its own record before the server is reachable; when the start ends without
// a reachable server (the server ended first because it lost the socket to an earlier
// one that is still running, or it never answered, or the caller gave up) the file
// must name the earlier server again, or none when there was no file, and the new
// process must be gone.
func TestStartThatDoesNotComeUpKeepsTheRecordOfTheRunningServer(t *testing.T) {
	const earlier = `{"pid":4242,"address":"/earlier.sock","start_key":"earlier-key","work_dir":"/earlier"}`
	for _, how := range []string{"the server exits", "the server never answers", "the caller gives up"} {
		for _, previous := range []string{earlier, ""} { // empty: no state file before the start
			name := how + ", no earlier record"
			if previous != "" {
				name = how + ", an earlier record"
			}
			t.Run(name, func(t *testing.T) {
				dir := shortTempDir(t)
				path := filepath.Join(dir, stateFileName)
				if previous != "" {
					if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				pidFile := filepath.Join(t.TempDir(), "pid")
				env := hangFileEnv + "=" + pidFile
				if how == "the server exits" {
					env = exitEnv + "=1"
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				srv := New(Config{
					Program:      os.Args[0],
					StateDir:     dir,
					Environ:      append(os.Environ(), env),
					StartTimeout: 500 * time.Millisecond,
				})
				srv.userCacheDir = func() (string, error) { return t.TempDir(), nil }
				srv.probe = func(context.Context, appserver.Endpoint) error {
					if how == "the caller gives up" {
						cancel()
					}
					return errors.New("not up")
				}

				_, err := srv.start(ctx)
				if how == "the caller gives up" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("start: %v, want the caller's cancellation", err)
					}
				} else if !errors.Is(err, ErrStartFailed) {
					t.Fatalf("start: %v, want the start to fail", err)
				}
				raw, err := os.ReadFile(path)
				if previous == "" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("state file after the start: %q (%v), want none", raw, err)
					}
				} else if err != nil || string(raw) != previous {
					t.Fatalf("state file after the start: %q (%v), want the earlier record %q", raw, err, previous)
				}
				if how != "the server exits" {
					pid, err := os.ReadFile(pidFile)
					if err != nil {
						t.Fatalf("the stand-in did not run: %v", err)
					}
					n, _ := strconv.Atoi(string(pid))
					if _, found, err := srv.lookup(n); err == nil && found {
						t.Errorf("the stand-in (pid %d) still runs after the start ended", n)
					}
				}
			})
		}
	}
}
