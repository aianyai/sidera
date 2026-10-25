package fakectl

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// TestAHandlerRegisteredWhileAConnectionIsBeingMadeReachesIt: newConn copies the
// handlers before the connection is listed, so a handler registered between the two
// steps is on neither list Handle walks. Listing the connection installs what has
// been registered since, under the lock Handle holds.
func TestAHandlerRegisteredWhileAConnectionIsBeingMadeReachesIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, err := Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	serverEnd, clientEnd := net.Pipe()
	c, err := srv.newConn(serverEnd) // the handlers are copied here: there are none yet
	if err != nil {
		t.Fatal(err)
	}
	srv.Handle("late", func(context.Context, *Conn, json.RawMessage) (any, error) { return struct{}{}, nil })
	c.p.Start()
	if !srv.adopt(c) {
		t.Fatal("the server refused a connection while it was open")
	}

	client, err := peer.New(clientEnd, clientEnd, peer.Config{})
	if err != nil {
		t.Fatal(err)
	}
	client.Start()
	defer client.Close()
	if err := client.Call(ctx, MethodHello, HelloParams{Role: RoleAdapter, ID: "x", PID: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(ctx, "late", nil, nil); err != nil {
		t.Fatalf("the handler registered while the connection was being made does not reach it: %v", err)
	}
}
