package adapterhost

import (
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// OfflineAfterPingFailures is how many pings in a row must go unanswered before
// an adapter is judged offline. It is a rule of the host and not a setting: one
// lost ping is a busy adapter, two is one that does not answer.
const OfflineAfterPingFailures = 2

// Params are the tunables of the host. A zero field takes the default of
// DefaultParams: nothing here is security relevant, and zero never means "no
// limit". Every duration is measured on the clock the host was given: the real
// clock in the daemon, the test clock where a test drives time itself.
type Params struct {
	// NotificationBuffer is how many notifications of one connection wait for the
	// engine; zero means 1024. While the buffer is full the notification handler
	// blocks, the peer's own queue fills and the peer ends the connection.
	NotificationBuffer int
	// PingInterval is the time between pings of an adapter in service; zero means
	// 30 seconds.
	PingInterval time.Duration
	// PingTimeout is how long a ping may take to be answered; zero means 10
	// seconds.
	PingTimeout time.Duration
	// RestartBackoffMin and RestartBackoffMax bound the wait before an instance is
	// started again: it starts at the minimum, doubles after every failed start or
	// lost connection and stops at the maximum. Zero means 1 second and 60
	// seconds.
	RestartBackoffMin time.Duration
	RestartBackoffMax time.Duration
	// StableAfter is how long a connection must stay in service for the backoff to
	// begin again at its minimum; zero means 5 minutes.
	StableAfter time.Duration
	// HandshakeTimeout is how long initialize, adapter.start and adapter.probe may
	// each take to be answered; zero means 20 seconds.
	HandshakeTimeout time.Duration
	// StartWait is how long Start waits for the enabled instances to answer
	// adapter.start before it judges the rest offline and goes on; zero means 30
	// seconds.
	StartWait time.Duration
	// ShutdownWait is how long the host waits for an adapter to end after shutdown,
	// or after it was killed, before it goes on; zero means 5 seconds.
	ShutdownWait time.Duration
	// Peer sets the limits of each protocol connection; zero fields take the
	// peer's defaults.
	Peer peer.Params
}

// DefaultParams returns the defaults.
func DefaultParams() Params {
	return Params{
		NotificationBuffer: 1024,
		PingInterval:       30 * time.Second,
		PingTimeout:        10 * time.Second,
		RestartBackoffMin:  time.Second,
		RestartBackoffMax:  time.Minute,
		StableAfter:        5 * time.Minute,
		HandshakeTimeout:   20 * time.Second,
		StartWait:          30 * time.Second,
		ShutdownWait:       5 * time.Second,
	}
}
