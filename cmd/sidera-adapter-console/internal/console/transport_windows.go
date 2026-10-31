//go:build windows

package console

import (
	"context"
	"net"
)

// On Windows the control channel is a loopback TCP port with a token; see
// transport_tcp.go. A named pipe would need its own security descriptor and a
// check of the server, and the state directory, which the daemon makes owner-only,
// already keeps the token from anyone else.

func listenLocal(stateDir string) (net.Listener, error) { return listenTokenTCP(stateDir) }

func dialLocal(ctx context.Context, stateDir string) (net.Conn, error) {
	return dialTokenTCP(ctx, stateDir)
}
