package ipc

import (
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
