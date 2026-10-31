package harness

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
)

// FreePort returns a port that nothing uses now, for TCP and UDP on every
// interface, for a data directory that init makes to listen on. A daemon listens
// where its network.json says, and the default port is one per machine, so two
// daemons of tests that run side by side (the packages of a test run are run in
// parallel) each need a port of their own.
//
// The port is picked below the range the system gives to outgoing connections: a
// port the system hands out for a listener could be the source port of another
// process's connection a moment later, which makes the daemon's bind fail. It is
// free when this returns, not reserved: a taker in the instant between is rare,
// and its effect is a start that fails naming the address.
func FreePort() (int, error) {
	for range 200 {
		port := 20000 + rand.IntN(10000)
		tcp, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			continue
		}
		udp, err := net.ListenPacket("udp", fmt.Sprintf("0.0.0.0:%d", port))
		tcp.Close()
		if err != nil {
			continue
		}
		udp.Close()
		return port, nil
	}
	return 0, errors.New("harness: no free port found between 20000 and 29999")
}
