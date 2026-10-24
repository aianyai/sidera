//go:build darwin || linux

package ipc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func locate(t *testing.T, home string, kind Kind) Address {
	t.Helper()
	addr, err := Locate(home, kind)
	if err != nil {
		t.Fatalf("Locate(%q, %s): %v", home, kind, err)
	}
	return addr
}

// listenIn prepares the socket directory of the interface as init would and
// starts a listener on it.
func listenIn(t *testing.T, home string, kind Kind, opts Options) (Address, net.Listener) {
	t.Helper()
	dir, _ := kind.socketDir()
	if _, err := os.Stat(filepath.Join(home, dir)); err != nil {
		mkdirExact(t, filepath.Join(home, dir), 0o700)
	}
	addr := locate(t, home, kind)
	l, err := Listen(addr, heldLock(true), opts)
	if err != nil {
		t.Fatalf("Listen(%s): %v", addr.Name(), err)
	}
	t.Cleanup(func() { l.Close() })
	return addr, l
}

func TestLocateUnixPaths(t *testing.T) {
	home := shortHome(t)
	if got, want := locate(t, home, KindAgent).Name(), filepath.Join(home, "run", "agent.sock"); got != want {
		t.Errorf("agent socket = %s, want %s", got, want)
	}
	if got, want := locate(t, home, KindManage).Name(), filepath.Join(home, "manage", "manage.sock"); got != want {
		t.Errorf("manage socket = %s, want %s (outside run/, which sandboxes grant to agents)", got, want)
	}

	t.Run("a relative or unclean home gives the same address as its clean absolute form", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(wd, home)
		if err != nil {
			t.Skip("no relative path to the temporary directory")
		}
		for _, h := range []string{rel, home + "/", home + "/./x/.."} {
			if got, want := locate(t, h, KindAgent).Name(), locate(t, home, KindAgent).Name(); got != want {
				t.Errorf("Locate(%q) = %s, want %s", h, got, want)
			}
		}
	})
	t.Run("an empty home is refused", func(t *testing.T) {
		if _, err := Locate("", KindAgent); err == nil {
			t.Fatal("Locate accepted an empty data directory")
		}
	})
	t.Run("an unknown interface is refused", func(t *testing.T) {
		if _, err := Locate(home, Kind("other")); err == nil {
			t.Fatal("Locate accepted an unknown interface")
		}
	})
}

func TestLocateRefusesPathLongerThanThePlatformAllows(t *testing.T) {
	long := "/" + strings.Repeat("a", platformSocketPathLimit)
	_, err := Locate(long, KindAgent)
	var pe *PathTooLongError
	if !errors.As(err, &pe) {
		t.Fatalf("Locate(%d-byte home) = %v, want *PathTooLongError", len(long), err)
	}
	if pe.Limit != platformSocketPathLimit || !strings.HasPrefix(pe.Path, long) || pe.Length != len(pe.Path) {
		t.Fatalf("error = %+v, want this platform's limit %d and the full path", pe, platformSocketPathLimit)
	}
}

func TestLocateAcceptsALongestFittingPath(t *testing.T) {
	suffix := "/run/agent.sock"
	home := "/" + strings.Repeat("a", platformSocketPathLimit-len(suffix)-1)
	addr, err := Locate(home, KindAgent)
	if err != nil {
		t.Fatalf("a path of exactly %d bytes was refused: %v", platformSocketPathLimit, err)
	}
	if len(addr.Name()) != platformSocketPathLimit {
		t.Fatalf("test built a %d-byte path, want %d", len(addr.Name()), platformSocketPathLimit)
	}
}

func TestListenCreatesAnOwnerOnlySocket(t *testing.T) {
	home := shortHome(t)
	for _, kind := range []Kind{KindAgent, KindManage} {
		addr, _ := listenIn(t, home, kind, Options{})
		fi, err := os.Lstat(addr.Name())
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want a socket with mode 0600", addr.Name(), fi.Mode())
		}
		if uid, _ := ownerUID(fi); uid != uint32(os.Geteuid()) {
			t.Errorf("%s is owned by uid %d, want %d", addr.Name(), uid, os.Geteuid())
		}
	}
}

func TestListenRefusesAnUnsafeSocketDirectory(t *testing.T) {
	t.Run("the directory does not exist", func(t *testing.T) {
		err := listenError(t, locate(t, shortHome(t), KindAgent), heldLock(true), Options{})
		assertPeerCheck(t, err, FailDirectoryMissing)
	})
	t.Run("the directory is open to others", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o755)
		assertPeerCheck(t, listenError(t, locate(t, home, KindAgent), heldLock(true), Options{}), FailDirectoryMode)
	})
	t.Run("the directory belongs to another user", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o700)
		opts := Options{files: foreignOwner{root: home}}
		assertPeerCheck(t, listenError(t, locate(t, home, KindAgent), heldLock(true), opts), FailDirectoryOwner)
	})
	t.Run("the directory is a symbolic link", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "elsewhere"), 0o700)
		if err := os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, "run")); err != nil {
			t.Fatal(err)
		}
		assertPeerCheck(t, listenError(t, locate(t, home, KindAgent), heldLock(true), Options{}), FailDirectorySymlink)
	})
}

func listenError(t *testing.T, addr Address, lock InstanceLock, opts Options) error {
	t.Helper()
	l, err := Listen(addr, lock, opts)
	if err == nil {
		l.Close()
		t.Fatalf("Listen(%s) succeeded, want a failure", addr.Name())
	}
	return err
}

// makeStaleSocket leaves a socket file with nobody behind it, as a daemon that
// was killed does.
func makeStaleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the stale socket was not left behind: %v", err)
	}
}

func TestListenRemovesAStaleSocketOnlyWhileHoldingTheLock(t *testing.T) {
	home := shortHome(t)
	mkdirExact(t, filepath.Join(home, "run"), 0o700)
	addr := locate(t, home, KindAgent)
	makeStaleSocket(t, addr.Name())

	for _, lock := range []InstanceLock{nil, heldLock(false)} {
		if err := listenError(t, addr, lock, Options{}); !errors.Is(err, ErrLockNotHeld) {
			t.Fatalf("Listen without the lock: %v, want ErrLockNotHeld", err)
		}
		if _, err := os.Lstat(addr.Name()); err != nil {
			t.Fatalf("the stale socket was deleted without the lock: %v", err)
		}
	}

	l, err := Listen(addr, heldLock(true), Options{})
	if err != nil {
		t.Fatalf("Listen with the lock over a stale socket: %v", err)
	}
	defer l.Close()
	conn, err := Dial(context.Background(), addr, Options{})
	if err != nil {
		t.Fatalf("Dial after replacing the stale socket: %v", err)
	}
	conn.Close()
}

func TestListenDoesNotDeleteWhatIsNotASocket(t *testing.T) {
	home := shortHome(t)
	mkdirExact(t, filepath.Join(home, "run"), 0o700)
	addr := locate(t, home, KindAgent)
	if err := os.WriteFile(addr.Name(), []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPeerCheck(t, listenError(t, addr, heldLock(true), Options{}), FailSocketNotSocket)
	if data, err := os.ReadFile(addr.Name()); err != nil || string(data) != "precious" {
		t.Fatalf("the file at the socket path was touched: %q, %v", data, err)
	}
}

func TestListenDoesNotDeleteASocketOfAnotherUser(t *testing.T) {
	home := shortHome(t)
	mkdirExact(t, filepath.Join(home, "run"), 0o700)
	addr := locate(t, home, KindAgent)
	makeStaleSocket(t, addr.Name())
	opts := Options{files: foreignOwner{root: home, only: "run/agent.sock"}}
	assertPeerCheck(t, listenError(t, addr, heldLock(true), opts), FailSocketOwner)
	if _, err := os.Lstat(addr.Name()); err != nil {
		t.Fatalf("a socket that is not the user's was deleted: %v", err)
	}
}

func TestDialReachesTheListenerAcrossConsecutiveConnections(t *testing.T) {
	addr, l := listenIn(t, shortHome(t), KindManage, Options{})
	serveEcho(t, l)
	for i, msg := range []string{"first", "second", "third"} {
		conn, err := Dial(context.Background(), addr, Options{})
		if err != nil {
			t.Fatalf("connection %d: %v", i+1, err)
		}
		echoOnce(t, conn, msg)
		conn.Close()
	}
}

func TestDialReportsDaemonNotRunning(t *testing.T) {
	t.Run("nothing exists", func(t *testing.T) {
		assertNotRunning(t, dialError(t, locate(t, shortHome(t), KindAgent)))
	})
	t.Run("the directory exists but the socket does not", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o700)
		assertNotRunning(t, dialError(t, locate(t, home, KindAgent)))
	})
	t.Run("a socket is left but nobody listens", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o700)
		addr := locate(t, home, KindAgent)
		makeStaleSocket(t, addr.Name())
		err := dialError(t, addr)
		assertNotRunning(t, err)
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("%v: the system error should be kept as the cause", err)
		}
	})
}

func dialError(t *testing.T, addr Address) error {
	t.Helper()
	return dialErrorWith(t, addr, Options{})
}

func dialErrorWith(t *testing.T, addr Address, opts Options) error {
	t.Helper()
	conn, err := Dial(context.Background(), addr, opts)
	if err == nil {
		conn.Close()
		t.Fatalf("Dial(%s) succeeded, want a failure", addr.Name())
	}
	return err
}

func assertNotRunning(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("got %v, want ErrDaemonNotRunning", err)
	}
	if errors.Is(err, ErrPeerCheckFailed) {
		t.Fatalf("%v: a missing daemon must not read as a failed check", err)
	}
}

func TestClientRefusesASocketThatIsNotTheUsers(t *testing.T) {
	t.Run("the socket belongs to another user", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o700)
		srv := serveCounting(t, filepath.Join(home, "run", "agent.sock"))
		opts := Options{files: foreignOwner{root: home, only: "run/agent.sock"}}
		assertPeerCheck(t, dialErrorWith(t, locate(t, home, KindAgent), opts), FailSocketOwner)
		if n := srv.connectionsBeforeNow(t); n != 0 {
			t.Fatalf("the client connected %d time(s) before refusing", n)
		}
	})
	t.Run("the socket path is a symbolic link", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o700)
		mkdirExact(t, filepath.Join(home, "other"), 0o700)
		srv := serveCounting(t, filepath.Join(home, "other", "x.sock"))
		if err := os.Symlink(filepath.Join(home, "other", "x.sock"), filepath.Join(home, "run", "agent.sock")); err != nil {
			t.Fatal(err)
		}
		assertPeerCheck(t, dialError(t, locate(t, home, KindAgent)), FailSocketNotSocket)
		if n := srv.connectionsBeforeNow(t); n != 0 {
			t.Fatalf("the client followed the link and connected %d time(s)", n)
		}
	})
	t.Run("the socket path holds a regular file", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "run"), 0o700)
		if err := os.WriteFile(filepath.Join(home, "run", "agent.sock"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		assertPeerCheck(t, dialError(t, locate(t, home, KindAgent)), FailSocketNotSocket)
	})
	t.Run("the socket directory is a symbolic link", func(t *testing.T) {
		home := shortHome(t)
		mkdirExact(t, filepath.Join(home, "elsewhere"), 0o700)
		srv := serveCounting(t, filepath.Join(home, "elsewhere", "agent.sock"))
		if err := os.Symlink(filepath.Join(home, "elsewhere"), filepath.Join(home, "run")); err != nil {
			t.Fatal(err)
		}
		assertPeerCheck(t, dialError(t, locate(t, home, KindAgent)), FailDirectorySymlink)
		if n := srv.connectionsBeforeNow(t); n != 0 {
			t.Fatalf("the client followed the link and connected %d time(s)", n)
		}
	})
}

func TestClientRefusesAServerOfAnotherUser(t *testing.T) {
	home := shortHome(t)
	_, l := listenIn(t, home, KindAgent, Options{})
	serveEcho(t, l)
	addr := locate(t, home, KindAgent)
	other := func(*net.UnixConn) (uint32, error) { return uint32(os.Geteuid()) + 1, nil }
	assertPeerCheck(t, dialErrorWith(t, addr, Options{peerUID: other}), FailServerUID)

	unreadable := func(*net.UnixConn) (uint32, error) { return 0, errors.New("no credentials") }
	assertPeerCheck(t, dialErrorWith(t, addr, Options{peerUID: unreadable}), FailServerCredentials)
}

// What the kernel reports for a real connection between two processes of this
// user is this user: the one fact the rejections above depend on.
func TestPeerUIDOfARealConnectionIsTheCurrentUser(t *testing.T) {
	home := shortHome(t)
	_, l := listenIn(t, home, KindAgent, Options{})
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	client, err := net.Dial("unix", locate(t, home, KindAgent).Name())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, ok := <-accepted
	if !ok {
		t.Fatal("the listener accepted nothing")
	}
	defer server.Close()

	for name, c := range map[string]*net.UnixConn{"client": client.(*net.UnixConn), "server": server.(*net.UnixConn)} {
		uid, err := peerUID(c)
		if err != nil {
			t.Fatalf("peerUID on the %s side: %v", name, err)
		}
		if uid != uint32(os.Geteuid()) {
			t.Errorf("peerUID on the %s side = %d, want euid %d", name, uid, os.Geteuid())
		}
	}
}

func TestListenerTurnsAwayAPeerThatIsNotTheUser(t *testing.T) {
	for _, tc := range []struct {
		name string
		uid  uint32
		err  error
		log  string
	}{
		{"another user", uint32(os.Geteuid()) + 1, nil, "another user"},
		{"credentials cannot be read", 0, errors.New("no credentials"), "could not be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logBuf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuf, nil))
			var mu sync.Mutex
			calls := 0
			peer := func(c *net.UnixConn) (uint32, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls == 1 { // the first connection is the stranger
					return tc.uid, tc.err
				}
				return peerUID(c)
			}
			home := shortHome(t)
			_, l := listenIn(t, home, KindAgent, Options{Logger: logger, peerUID: peer})
			serveEcho(t, l)
			path := locate(t, home, KindAgent).Name()

			stranger, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer stranger.Close()
			stranger.SetReadDeadline(time.Now().Add(5 * time.Second))
			if n, err := stranger.Read(make([]byte, 1)); err != io.EOF && !errors.Is(err, syscall.ECONNRESET) {
				t.Fatalf("the stranger's connection was kept open: read %d bytes, %v; want it closed", n, err)
			}

			friend, err := net.Dial("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer friend.Close()
			echoOnce(t, friend, "still serving the user")

			mu.Lock()
			defer mu.Unlock()
			if calls < 2 {
				t.Fatalf("the credential check ran %d time(s), the injection point was not hit", calls)
			}
			if !strings.Contains(logBuf.String(), tc.log) {
				t.Fatalf("log %q lacks %q", logBuf.String(), tc.log)
			}
		})
	}
}

func TestClosingTheListenerRemovesTheSocket(t *testing.T) {
	home := shortHome(t)
	addr, l := listenIn(t, home, KindAgent, Options{})
	l.Close()
	if _, err := os.Lstat(addr.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after Close: %v, want it removed", err)
	}
}

// The listener returned by Listen offers only the checked Accept. The methods
// of net.UnixListener that would hand out a connection or the descriptor
// without the peer check must not be reachable through it.
func TestListenerDoesNotExposeAnUncheckedAccept(t *testing.T) {
	_, l := listenIn(t, shortHome(t), KindAgent, Options{})
	if _, ok := l.(interface{ AcceptUnix() (*net.UnixConn, error) }); ok {
		t.Error("the listener exposes AcceptUnix, which accepts without the peer check")
	}
	if _, ok := l.(interface{ File() (*os.File, error) }); ok {
		t.Error("the listener exposes File, which hands out the descriptor")
	}
	if _, ok := l.(interface{ SetUnlinkOnClose(bool) }); ok {
		t.Error("the listener exposes SetUnlinkOnClose")
	}
}
