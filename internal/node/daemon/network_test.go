package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/daemon"
)

// listening says whether something accepts TCP connections on the port of the
// machine's loopback.
func listening(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func wantNotListening(t testing.TB, port int, when string) {
	t.Helper()
	if listening(port) {
		t.Errorf("port %d accepts connections %s", port, when)
	}
}

// The node listens where network.json says, and only at the end of the start:
// when the hook of step 15 runs the engine serves and nothing listens yet, and
// when the shutdown reaches the hooks that follow the interfaces, the network is
// closed, whatever the order the interfaces stopped in.
func TestTheNetworkListensOnlyAfterTheNodeServesAndStopsWithTheInterfaces(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	port := c.InitPort
	var atServe, atStopInterfaces bool
	c.Attach.AgentServe = func(context.Context, *daemon.Env) error {
		atServe = listening(port)
		return nil
	}
	c.Attach.StopInterfaces = func(context.Context) error {
		atStopInterfaces = listening(port)
		return nil
	}
	d, err := daemon.Start(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !listening(port) {
		t.Error("the node does not listen when the start has returned")
	}
	d.RequestStop("test over")
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if atServe || atStopInterfaces {
		t.Errorf("listening at the hook of step 15: %v, after the interfaces stopped: %v; want neither", atServe, atStopInterfaces)
	}
	wantNotListening(t, port, "after the shutdown")
}

// A port that is in use fails the start, and the error says which address could
// not be opened; what the start had begun is shut down, so the same home starts
// when the port is free.
func TestAStartOnAPortInUseFailsNamingTheAddressAndLeavesNothingRunning(t *testing.T) {
	noLeaks(t)
	c, _ := initialized(t)
	// Match the configured IPv4 listener. On Windows a dual-stack wildcard
	// listener does not prevent a separate IPv4 socket from binding this port.
	blocker, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", c.InitPort))
	if err != nil {
		t.Skipf("the port picked for the test is taken: %v", err)
	}
	t.Cleanup(func() { blocker.Close() })
	d, err := daemon.Start(context.Background(), c)
	if d != nil {
		t.Cleanup(func() {
			d.RequestStop("test over")
			if err := d.Wait(); err != nil {
				t.Errorf("the unexpected daemon stopped with an error: %v", err)
			}
		})
	}
	blocker.Close()
	if err == nil {
		t.Fatal("the start succeeded on a port in use")
	}
	if want := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", c.InitPort); !strings.Contains(err.Error(), want) {
		t.Errorf("the error %q does not name the address %s", err, want)
	}
	var fatal *daemon.FatalError
	if errors.As(err, &fatal) {
		t.Error("a port in use is no failure of the disk")
	}
	started(t, c)
}
