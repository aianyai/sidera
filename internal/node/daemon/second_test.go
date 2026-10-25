package daemon_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aianyai/sidera/internal/node/daemon"
)

// Two daemons on one data directory would be two writers of the same state. The
// second one is refused before it touches anything, and the first goes on
// serving: its socket is the same file, its management interface answers, and
// nothing the second one did can be seen on the disk.
func TestSecondDaemonOnSameHomeRefused(t *testing.T) {
	c, _ := initialized(t)
	first := started(t, c)
	sock := filepath.Join(c.Home, "manage", "manage.sock")
	before, err := os.Lstat(sock)
	if runtime.GOOS != "windows" && err != nil {
		t.Fatalf("the first daemon has no management socket: %v", err)
	}

	_, err = daemon.Start(context.Background(), c)
	if !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("a second daemon on the same home: %v; want ErrAlreadyRunning", err)
	}

	if runtime.GOOS != "windows" {
		after, err := os.Lstat(sock)
		if err != nil || !os.SameFile(before, after) {
			t.Errorf("the socket of the first daemon was replaced or removed by the refused one: %v", err)
		}
	}
	if status, body := manage(t, c.Home, "endpoints.list", map[string]any{}); status != http.StatusOK {
		t.Errorf("the first daemon stopped answering: %d %s", status, body)
	}
	select {
	case <-first.Done():
		t.Error("the refused start stopped the first daemon")
	default:
	}
}

// init takes the same lock: it is refused while a daemon runs on the directory,
// so it cannot rewrite what a running node holds.
func TestInitOnTheHomeOfARunningDaemonIsRefused(t *testing.T) {
	c, _ := initialized(t)
	started(t, c)
	if _, err := daemon.Init(context.Background(), c); !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("init on the home of a running daemon: %v; want ErrAlreadyRunning", err)
	}
}
