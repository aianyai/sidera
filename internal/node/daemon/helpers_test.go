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
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/clock/clocktest"
	"github.com/aianyai/sidera/internal/node/daemon"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/ipc"
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
	return rand.NewChaCha8(s)
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
	addr, err := ipc.Locate(home, ipc.KindManage)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return ipc.Dial(ctx, addr, ipc.Options{}) },
	}, Timeout: 30 * time.Second}
	defer client.CloseIdleConnections()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post("http://sidera/v1/manage/"+method, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the answer to %s: %v", method, err)
	}
	return resp.StatusCode, out
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
