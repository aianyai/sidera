package console

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// The control channel as a loopback TCP port guarded by a token. The adapter picks
// the port and a random token, and writes both to a file in the state directory,
// which is reachable by the owner alone; a client reads the file and sends the
// token as the first line of its connection. A process that cannot read the file
// cannot get a connection served. The file is replaced at every start and removed
// when the listener closes. A process of another user could still take the port
// while the adapter is down and be sent the token of a client that reads a stale
// file; the token is for the adapter's own run, so it gets nothing it could use
// afterwards.
//
// Windows uses it (transport_windows.go); it is portable code so that the tests
// run it everywhere.

const addrFileName = "console.addr"

type tcpEndpoint struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
}

func listenTokenTCP(stateDir string) (net.Listener, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening on a loopback port: %w", err)
	}
	file := filepath.Join(stateDir, addrFileName)
	data, err := json.Marshal(tcpEndpoint{Addr: l.Addr().String(), Token: token})
	if err == nil {
		err = writeFile(file, data)
	}
	if err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("writing %s: %w", file, err)
	}
	return &tokenListener{Listener: l, token: token, file: file}, nil
}

// writeFile replaces the file in one step, owner-only: a reader never sees half of it.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func dialTokenTCP(ctx context.Context, stateDir string) (net.Conn, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, addrFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNotRunning
	} else if err != nil {
		return nil, err
	}
	var ep tcpEndpoint
	if err := json.Unmarshal(data, &ep); err != nil {
		return nil, fmt.Errorf("reading %s: %w", addrFileName, err)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", ep.Addr)
	if err != nil {
		// The file names a port on this machine, so a failure to reach it is a
		// stale file: the adapter that wrote it is gone.
		return nil, fmt.Errorf("%w (%v)", errNotRunning, err)
	}
	if _, err := io.WriteString(conn, ep.Token+"\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// tokenListener hands out connections that check the token on their first read,
// which happens in the goroutine that serves the connection and under its
// deadline: a client that sends nothing holds up nobody else.
type tokenListener struct {
	net.Listener
	token string
	file  string
}

func (l *tokenListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &tokenConn{Conn: c, token: l.token}, nil
}

func (l *tokenListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.file)
	return err
}

type tokenConn struct {
	net.Conn
	token string

	once sync.Once
	err  error
}

var errBadToken = errors.New("the connection did not present the token")

func (c *tokenConn) Read(p []byte) (int, error) {
	c.once.Do(func() {
		line := make([]byte, len(c.token)+1)
		if _, err := io.ReadFull(c.Conn, line); err != nil {
			c.err = errBadToken
		} else if subtle.ConstantTimeCompare(line[:len(c.token)], []byte(c.token)) != 1 || line[len(c.token)] != '\n' {
			c.err = errBadToken
		}
		if c.err != nil {
			_ = c.Conn.Close()
		}
	})
	if c.err != nil {
		return 0, c.err
	}
	return c.Conn.Read(p)
}
