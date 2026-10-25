package adapterhost

import (
	"fmt"
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

// withDefaults returns p with every zero field replaced by its default.
func (p Params) withDefaults() Params {
	d := DefaultParams()
	if p.NotificationBuffer == 0 {
		p.NotificationBuffer = d.NotificationBuffer
	}
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{
		{&p.PingInterval, d.PingInterval},
		{&p.PingTimeout, d.PingTimeout},
		{&p.RestartBackoffMin, d.RestartBackoffMin},
		{&p.RestartBackoffMax, d.RestartBackoffMax},
		{&p.StableAfter, d.StableAfter},
		{&p.HandshakeTimeout, d.HandshakeTimeout},
		{&p.StartWait, d.StartWait},
		{&p.ShutdownWait, d.ShutdownWait},
	} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}
	// A cap below the first wait would make the wait shrink after a failure.
	p.RestartBackoffMax = max(p.RestartBackoffMax, p.RestartBackoffMin)
	return p
}

// validate refuses a negative field: zero means the default, and a negative
// number is a mistake that would otherwise become an instant timer or an empty
// buffer. The peer's own limits are checked by the peer when a connection is made.
func (p Params) validate() error {
	for _, f := range []struct {
		name string
		neg  bool
	}{
		{"NotificationBuffer", p.NotificationBuffer < 0},
		{"PingInterval", p.PingInterval < 0},
		{"PingTimeout", p.PingTimeout < 0},
		{"RestartBackoffMin", p.RestartBackoffMin < 0},
		{"RestartBackoffMax", p.RestartBackoffMax < 0},
		{"StableAfter", p.StableAfter < 0},
		{"HandshakeTimeout", p.HandshakeTimeout < 0},
		{"StartWait", p.StartWait < 0},
		{"ShutdownWait", p.ShutdownWait < 0},
	} {
		if f.neg {
			return fmt.Errorf("adapterhost: %s is negative", f.name)
		}
	}
	return nil
}
