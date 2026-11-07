package localhttp

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/ipc"
)

// A dial that fails obtained no connection, so no connection was lost and no
// request could have been sent: it keeps the words apiwire has for it and is not
// an unknown outcome. (A connection that is obtained and then lost is covered
// from outside the package, where a real daemon closes it.)
func TestADialThatFailsIsNotAnUnknownOutcome(t *testing.T) {
	// Only the length of the path matters: nothing is dialed, so the directory is
	// short for the limit on a socket path and holds no socket.
	home, err := os.MkdirTemp("", "lh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	c, err := NewClient(ClientConfig{Home: home, Kind: ipc.KindManage, PathPrefix: "/v1/manage/"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("ipc: dial: too many open files")
	}
	_, err = c.Call(context.Background(), Request{Method: "known", Body: struct{}{}, Timeout: 10 * time.Second})
	if e := apiwire.FromError(err); e.Category == apiwire.CategoryOutcomeUnknown {
		t.Fatalf("Call: %v; a dial that obtained no connection must not be an unknown outcome", err)
	}
}
