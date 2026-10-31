package console

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokenTransportServesOnlyAClientThatPresentsTheToken(t *testing.T) {
	dir := t.TempDir()
	l, err := listenTokenTCP(dir)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err == nil {
					_, _ = c.Write(buf)
				}
			}()
		}
	}()

	ctx := context.Background()
	c, err := dialTokenTCP(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = c.Write([]byte("ping"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "ping" {
		t.Errorf("echo with the token: %q, %v", got, err)
	}
	c.Close()

	// A connection that does not present the token is closed without an answer.
	ep, _ := os.ReadFile(filepath.Join(dir, addrFileName))
	raw, err := net.Dial("tcp", jsonField(t, ep, "addr"))
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = raw.Write([]byte("0000000000000000000000000000000000000000000000000000000000000000\nping"))
	if n, err := raw.Read(make([]byte, 4)); n != 0 || err == nil {
		t.Errorf("a wrong token was answered: %d bytes, %v", n, err)
	}
	raw.Close()

	l.Close()
	if _, err := os.Stat(filepath.Join(dir, addrFileName)); !os.IsNotExist(err) {
		t.Errorf("the address file outlived the listener: %v", err)
	}
	if _, err := dialTokenTCP(ctx, dir); err == nil {
		t.Error("dialing after the listener closed succeeded")
	}
}

func jsonField(t *testing.T, data []byte, name string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m[name]
}
