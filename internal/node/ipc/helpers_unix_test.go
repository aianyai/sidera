//go:build unix

package ipc

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
)

// shortHome returns a fresh data directory whose socket paths fit the
// platform: the default temporary directory on macOS is too long for them.
func shortHome(t *testing.T) string {
	t.Helper()
	for _, base := range []string{"", "/tmp"} {
		dir, err := os.MkdirTemp(base, "ipc")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		// The longest socket path below it must fit the smaller limit.
		if len(filepath.Join(dir, "manage", "manage.sock")) <= darwinSocketPathLimit {
			return dir
		}
	}
	t.Skip("no temporary directory short enough for a Unix socket path")
	return ""
}

// mkdirExact creates a directory with exactly the given permission bits,
// whatever the umask is.
func mkdirExact(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, perm); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// foreignOwner presents the same files as the real file system, except that
// every one of them looks owned by a different user.
type foreignOwner struct{ root string }

func (f foreignOwner) Lstat(name string) (fs.FileInfo, error) {
	fi, err := os.Lstat(filepath.Join(f.root, filepath.FromSlash(name)))
	if err != nil {
		return nil, err
	}
	st := *fi.Sys().(*syscall.Stat_t)
	st.Uid = uint32(os.Geteuid()) + 1
	return foreignInfo{FileInfo: fi, sys: &st}, nil
}

type foreignInfo struct {
	fs.FileInfo
	sys *syscall.Stat_t
}

func (i foreignInfo) Sys() any { return i.sys }

// countingServer accepts connections on a socket and closes each at once. It
// lets a test prove that a client never connected.
type countingServer struct {
	l        net.Listener
	path     string
	accepted atomic.Int32
	wg       sync.WaitGroup
}

func serveCounting(t *testing.T, path string) *countingServer {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	s := &countingServer{l: l, path: path}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			c.Close()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		s.wg.Wait()
	})
	return s
}

// connectionsBeforeNow connects once itself and returns how many connections
// the server had accepted before that one. Connections are accepted in order,
// so the answer is exact once the probe's own connection has been closed by
// the server.
func (s *countingServer) connectionsBeforeNow(t *testing.T) int {
	t.Helper()
	c, err := net.Dial("unix", s.path)
	if err != nil {
		t.Fatalf("probe connection to %s: %v", s.path, err)
	}
	defer c.Close()
	buf := make([]byte, 1)
	c.Read(buf) // returns when the server has accepted and closed the probe
	return int(s.accepted.Load()) - 1
}
