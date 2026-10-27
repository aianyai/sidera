package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	nodeconfig "github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/ipc"
	"github.com/aianyai/sidera/internal/node/keys"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/txn"
)

// t0 is the origin of the test clock.
var t0 = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// socketPathLimit is the shortest socket path limit of the platforms the tests
// run on (macOS, 103 bytes): the default temporary directory there is too long
// for the longest socket path below a data directory.
const socketPathLimit = 103

// newHome returns the path of a data directory that does not exist yet, in a
// temporary directory short enough for the socket paths below it.
func newHome(t testing.TB) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return filepath.Join(t.TempDir(), "home")
	}
	for _, base := range []string{"", "/tmp"} {
		dir, err := os.MkdirTemp(base, "sd")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		home := filepath.Join(dir, "h")
		if len(filepath.Join(home, "manage", "manage.sock")) <= socketPathLimit {
			return home
		}
	}
	t.Skip("no temporary directory short enough for a Unix socket path")
	return ""
}

// seeded is a reader of reproducible bytes, so that keys and tokens are the same
// on every run.
func seeded(seed byte) io.Reader {
	var s [32]byte
	s[0] = seed
	return &lockedReader{r: rand.NewChaCha8(s)}
}

// lockedReader makes a reader safe for the several goroutines that read the
// randomness of a node.
type lockedReader struct {
	mu sync.Mutex
	r  io.Reader
}

func (l *lockedReader) Read(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.Read(p)
}

// config is the configuration of a daemon over home with a test clock.
func config(home string, clk *clocktest.Fake) daemon.Config {
	return daemon.Config{Home: home, Clock: clk, Rand: seeded(1)}
}

// initialized makes a data directory with init and returns its configuration.
func initialized(t testing.TB) (daemon.Config, *clocktest.Fake) {
	t.Helper()
	clk := clocktest.NewFake(t0)
	c := config(newHome(t), clk)
	if _, err := daemon.Init(context.Background(), c); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return c, clk
}

// started runs a daemon over an initialized home and stops it when the test ends.
func started(t testing.TB, c daemon.Config) *daemon.Daemon {
	t.Helper()
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		d.RequestStop("test over")
		if err := d.Wait(); err != nil {
			t.Errorf("the daemon stopped with an error: %v", err)
		}
	})
	return d
}

// manage sends one request to the management interface of the daemon over home,
// the way a client does: through ipc.Dial, which checks the way to the socket
// and the process behind it.
func manage(t testing.TB, home, method string, body any) (int, []byte) {
	t.Helper()
	status, out, err := doManage(home, method, body)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	return status, out
}

// tryManage says what the interface answered to a read, and 0 when it did not
// answer: for a test that waits for the daemon, or that expects it to be gone.
func tryManage(home, method string) int {
	status, _, err := doManage(home, method, map[string]any{})
	if err != nil {
		return 0
	}
	return status
}

func doManage(home, method string, body any) (int, []byte, error) {
	addr, err := ipc.Locate(home, ipc.KindManage)
	if err != nil {
		return 0, nil, err
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ipc.Dial(ctx, addr, ipc.Options{}) },
	}, Timeout: 30 * time.Second}
	defer client.CloseIdleConnections()
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Post("http://sidera/v1/manage/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return resp.StatusCode, out, err
}

// memConfig is a configuration over a file system in memory: the disk of a test
// that crashes, or injects a fault into, what the daemon writes. The home is
// still a path on the real disk, because the sockets are.
func memConfig(home string, clk *clocktest.Fake, fsys fsx.FS) daemon.Config {
	c := config(home, clk)
	c.FS = fsys
	return c
}

// snapshot lists every file and directory below root with its mode and content.
func snapshot(t testing.TB, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			out[rel+"/"] = info.Mode().String()
		case info.Mode()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = info.Mode().String() + " -> " + target
		case info.Mode()&fs.ModeSocket != 0:
			out[rel] = info.Mode().String()
		case rel == "daemon.lock":
			// The instance lock is never written, and on Windows a running daemon's
			// lock (LockFileEx on its first byte) is mandatory: another handle cannot
			// read the file, so its presence and mode are what there is to compare.
			out[rel] = info.Mode().String()
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = info.Mode().String() + " " + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

// logLines are the lines of logs/daemon.log of a home.
func logLines(t testing.TB, home string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "logs", "daemon.log"))
	if err != nil {
		t.Fatalf("reading the log of the daemon: %v", err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// steps picks the "startup step N begin|end" and "shutdown step N begin|end" lines
// out of the log, as "startup N begin" and so on, in order.
func steps(lines []string) []string {
	var out []string
	for _, l := range lines {
		for _, kind := range []string{"startup", "shutdown"} {
			if i := strings.Index(l, "msg=\""+kind+" step "); i >= 0 {
				rest := strings.TrimPrefix(l[i:], "msg=\""+kind+" step ")
				fields := strings.Fields(strings.SplitN(rest, "\"", 2)[0])
				if len(fields) == 2 {
					out = append(out, kind+" "+fields[0]+" "+fields[1])
				}
			}
		}
	}
	return out
}

// requireUnix skips a test that is about permission bits and symbolic links,
// which the Windows tests check in their own files.
func requireUnix(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("permission bits and symbolic links of Unix; the Windows checks are in the files of that platform")
	}
}

// seedHome puts an adapter instance in the configuration and endpoint records on
// the disk of an initialized home, the way a node that has run has them, while no
// daemon runs over it. The endpoints belong to an instance the owner has enabled,
// so the start does not close them as the endpoints of a disabled one.
func seedHome(t testing.TB, home string, recs ...store.EndpointRecord) {
	t.Helper()
	ins, err := nodeconfig.Instances{Version: 1, Items: map[string]nodeconfig.Instance{
		"chat": {Adapter: "chat", Enabled: true},
	}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "instances.json"), ins, 0o600); err != nil {
		t.Fatal(err)
	}
	seedStore(t, home, func(st *store.Store) {
		for _, r := range recs {
			if err := st.EndpointUpdate(r); err != nil {
				t.Fatalf("seeding %s: %v", r.ID, err)
			}
		}
	})
}

// seedStore lets write put records through the store on the disk of an initialized
// home while no daemon runs over it.
func seedStore(t testing.TB, home string, write func(st *store.Store)) {
	t.Helper()
	fsys, err := fsx.OpenOS(home, seeded(9))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := fsys.Lock("daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	tab, err := daemon.Table()
	if err != nil {
		t.Fatal(err)
	}
	w := txn.New(fsys, tab, nil)
	if err := w.Recover(); err != nil {
		t.Fatal(err)
	}
	write(store.New(w))
}

func endpointID(t testing.TB, b byte) ident.EndpointID {
	t.Helper()
	id, err := ident.EndpointIDFromBytes(bytes.Repeat([]byte{b}, ident.EndpointIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// mkSocketDirs makes the directories of the sockets on the real disk, for a test
// whose state lives in memory.
func mkSocketDirs(t testing.TB, home string) {
	t.Helper()
	for _, d := range []string{"", "manage", "run"} {
		p := filepath.Join(home, d)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// noLeaks fails the test when goroutines that it started are still running at its
// end: the daemon owns every goroutine it starts and stops it on shutdown.
func noLeaks(t testing.TB) {
	t.Helper()
	before, _ := ownGoroutines()
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			n, stacks := ownGoroutines()
			if n <= before {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("%d goroutines running, %d before the test:\n%s", n, before, stacks)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// processGoroutines are the functions of goroutines that a library starts once for
// the whole process, the first time it is used, and never ends. The first test that
// uses the library would otherwise be blamed for them. go-winio, which carries the
// named pipes of the management interface on Windows, waits on its I/O completion
// port in one such goroutine.
var processGoroutines = []string{
	"github.com/Microsoft/go-winio.ioCompletionProcessor(",
}

// ownGoroutines counts the running goroutines that are not the process's own (see
// processGoroutines) and returns the stacks of every goroutine, for the report.
func ownGoroutines() (int, []byte) {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range bytes.Split(buf, []byte("\n\n")) {
		if !processGoroutine(g) {
			count++
		}
	}
	return count, buf
}

func processGoroutine(stack []byte) bool {
	for _, f := range processGoroutines {
		if bytes.Contains(stack, []byte(f)) {
			return true
		}
	}
	return false
}

// nodeOf is the identifier of the node of an initialized home, as init said it.
func nodeOf(t testing.TB, c daemon.Config) ident.NodeID {
	t.Helper()
	fsys, err := fsx.OpenOS(c.Home, seeded(3))
	if err != nil {
		t.Fatal(err)
	}
	sr, err := keys.ReadSelfRevocation(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return sr.Node()
}

// nodeOfOther is some other node.
func nodeOfOther(t testing.TB) ident.NodeID {
	t.Helper()
	n, err := ident.NodeIDFromBytes(bytes.Repeat([]byte{0x2a}, ident.NodeIDLen))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
