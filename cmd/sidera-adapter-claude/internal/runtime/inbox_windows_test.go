//go:build windows

package runtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// pipeServer stands in for the inbox pipe of Claude Code on Windows: one instance
// at a time, a connection that must open with the right authentication line.
type pipeServer struct {
	name  string
	lines chan string
}

func newPipeServer(t *testing.T, token string) *pipeServer {
	t.Helper()
	s := &pipeServer{name: fmt.Sprintf(`\\.\pipe\LOCAL\cc-msg-test-%d-%d`, os.Getpid(), time.Now().UnixNano()), lines: make(chan string, 8)}
	name, err := windows.UTF16PtrFromString(s.name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// One file owns the handle for the life of the server: a second os.NewFile on
	// the same handle has a finalizer of its own that would close it, and the cleanup
	// closes it once.
	f := os.NewFile(uintptr(h), s.name)
	var stopping atomic.Bool
	go func() {
		defer close(s.lines)
		for !stopping.Load() {
			// A client that connected, wrote and closed before the server got here is
			// answered with ERROR_PIPE_CONNECTED or ERROR_NO_DATA; what it wrote is
			// still in the pipe and is read below.
			if err := windows.ConnectNamedPipe(h, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) && !errors.Is(err, windows.ERROR_NO_DATA) {
				return
			}
			sc := bufio.NewScanner(f)
			first := true
			for sc.Scan() {
				if first && sc.Text() != `{"type":"auth","token":"`+token+`"}` {
					break // a wrong first line closes the connection, delivering nothing
				}
				first = false
				s.lines <- sc.Text()
			}
			_ = windows.DisconnectNamedPipe(h)
		}
	}()
	t.Cleanup(func() {
		// The handle is synchronous: closing it waits for a connect or a read that is
		// pending on it, so the pending call is cancelled until the loop has ended.
		stopping.Store(true)
		for ended := false; !ended; {
			_ = windows.CancelIoEx(h, nil)
			select {
			case _, ok := <-s.lines:
				ended = !ok
			case <-time.After(10 * time.Millisecond):
			}
		}
		_ = f.Close()
	})
	return s
}

func (s *pipeServer) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-s.lines:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("the server received nothing")
		return ""
	}
}

func TestInboxWritesTheAuthenticationLineBeforeTheMessageToAPipe(t *testing.T) {
	s := newPipeServer(t, "tok")
	const auth, msg = `{"type":"auth","token":"tok"}`, `{"type":"user","message":{"role":"user","content":"hi"},"uuid":"u"}`
	if err := (osInbox{}).Write(context.Background(), s.name, []byte(auth+"\n"+msg+"\n")); err != nil {
		t.Fatal(err)
	}
	if got := s.next(t); got != auth {
		t.Fatalf("first line %q", got)
	}
	if got := s.next(t); got != msg {
		t.Fatalf("second line %q", got)
	}
}

// A server that serves one client at a time answers a second one with ERROR_PIPE_BUSY;
// the write waits for it within the limit, and does not report it as unreachable.
func TestInboxWaitsForABusyPipe(t *testing.T) {
	s := newPipeServer(t, "tok")
	first, err := os.OpenFile(s.name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		first.Close()
	}()
	if err := (osInbox{}).Write(context.Background(), s.name, []byte(`{"type":"auth","token":"tok"}`+"\n"+`{"type":"user"}`+"\n")); err != nil {
		t.Fatalf("a busy pipe is waited for: %v", err)
	}
	s.next(t)
}

func TestInboxWithNoPipeIsUnreachableAndNamesNoPath(t *testing.T) {
	const name = `\\.\pipe\LOCAL\cc-msg-no-such-pipe`
	err := (osInbox{}).Write(context.Background(), name, []byte("x\n"))
	if !errors.Is(err, ErrInboxUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if want := "no-such-pipe"; strings.Contains(err.Error(), want) {
		t.Fatalf("the error names the pipe: %v", err)
	}
}
