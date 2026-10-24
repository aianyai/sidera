package ipc

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// heldLock is an InstanceLock whose answer the test chooses.
type heldLock bool

func (h heldLock) Held() bool { return bool(h) }

func echoOnce(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("echo = %q, %v, want %q", buf, err, msg)
	}
}

// serveEcho echoes every accepted connection until the listener is closed.
func serveEcho(t *testing.T, l net.Listener) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { l.Close(); wg.Wait() })
}

// assertPeerCheck requires a failed check, and not "daemon not running", which
// would send the user to start a daemon that someone else may have displaced.
func assertPeerCheck(t *testing.T, err error, want Failure) {
	t.Helper()
	if errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("got %q, a failed check must not read as daemon-not-running", err)
	}
	if !errors.Is(err, ErrPeerCheckFailed) {
		t.Fatalf("error %q does not match ErrPeerCheckFailed", err)
	}
	var pe *PeerCheckError
	if !errors.As(err, &pe) {
		t.Fatalf("error %q is not a *PeerCheckError", err)
	}
	if pe.Failure != want {
		t.Fatalf("failure = %q, want %q (%v)", pe.Failure, want, err)
	}
}
