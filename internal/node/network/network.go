package network

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/clock"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// The time a call may take, from the engine's point of view, and how long a
// node that could not be reached stays unreachable without another attempt.
const (
	// DefaultConnectTimeout is the budget for a connection to come up and its
	// certificate exchange to complete.
	DefaultConnectTimeout = 4 * time.Second
	// DefaultCallTimeout is the budget for one request and its answer once the
	// connection is up.
	DefaultCallTimeout = 10 * time.Second
	// DefaultUnreachableFor is how long a node that could not be connected is
	// answered ErrUnreachable at once. The engine's one outbound worker takes
	// its calls in order, so a node that is down must cost it milliseconds, not
	// the connect budget, on each resend.
	DefaultUnreachableFor = 10 * time.Second
	// DefaultTickEvery is how often the connection table is checked for
	// overdue connections.
	DefaultTickEvery = time.Second

	// maxStrangerSerials is how many serials of nodes that are not members the
	// network remembers for itself, in memory only.
	maxStrangerSerials = 1024
)

// DefaultParams returns the parameters of the connection accounting: at most 64
// unauthenticated and 8 non-member connections, a non-member lifetime of the
// exchange deadline plus the pairing online time plus 5 seconds of margin, and
// a sweep every 10 seconds.
func DefaultParams() preauth.Params {
	const pairingOnline = 30 * time.Second
	return preauth.Params{
		UnauthenticatedCap:   64,
		NonMemberConcurrency: 8,
		NonMemberLifetime:    preauth.ExchangeDeadline + pairingOnline + 5*time.Second,
		PairingOnlineTimeout: pairingOnline,
		SweepInterval:        10 * time.Second,
	}
}

// Config is what New needs. Zero durations and a zero Params take the defaults.
type Config struct {
	// Key is the node's network private key. The node's connections are
	// authenticated with it, and the certificate the node presents names it.
	Key ed25519.PrivateKey
	// Clock is the time of every deadline of the accounting and of the facts of
	// a decision. Timeouts of calls are contexts and run on the real clock.
	Clock clock.Clock
	// Params are the numbers of the connection accounting.
	Params preauth.Params
	// Log receives what the network layer says; nil discards it.
	Log *slog.Logger

	ConnectTimeout time.Duration
	CallTimeout    time.Duration
	// FetchTimeout bounds one pairing result fetch once its stream is open;
	// zero is the pairing package's own.
	FetchTimeout   time.Duration
	UnreachableFor time.Duration
	TickEvery      time.Duration
}

func (c *Config) setDefaults() {
	if c.Params == (preauth.Params{}) {
		c.Params = DefaultParams()
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	if c.CallTimeout == 0 {
		c.CallTimeout = DefaultCallTimeout
	}
	if c.FetchTimeout == 0 {
		c.FetchTimeout = pairing.DefaultParams().FetchTimeout
	}
	if c.UnreachableFor == 0 {
		c.UnreachableFor = DefaultUnreachableFor
	}
	if c.TickEvery == 0 {
		c.TickEvery = DefaultTickEvery
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
}

// Network is the node's libp2p host and everything built on it.
type Network struct {
	cfg   Config
	clock clock.Clock
	log   *slog.Logger
	peer  peer.ID

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // every goroutine the package starts

	lib     *libp2pHost
	limiter *peerLimiter
	// dialBacks is the set of askers this node is dialing back for.
	dialBacks dialBackSlots
	node      atomic.Pointer[nodeHolder]

	mu          sync.Mutex // guards everything below
	closed      bool
	acct        *preauth.Accounting
	conns       map[lnet.Conn]*connEntry
	connSeq     uint64                         // the last sequence number given to a connection
	byKey       map[preauth.ConnKey]*connEntry // the tracked connection of a key
	membership  *membership
	strangers   map[ident.NodeID]record.SerialSeen
	heldKeys    map[ident.PublicKey]struct{} // the held members given to the accounting
	heldExpiry  time.Time                    // when the held members change with time alone
	book        map[ident.NodeID][]ma.Multiaddr
	dialPeers   map[ident.NodeID][]peer.ID
	bookWritten map[peer.ID][]ma.Multiaddr // what the address book wrote to the peerstore, per peer
	unreachable map[ident.NodeID]time.Time
	callStreaks map[ident.NodeID]*callStreak // the run of failed calls to a node, until a call succeeds
}

type nodeHolder struct{ node Node }

// New builds the host and starts the connection checks. It listens on nothing:
// Listen opens the listeners, and outbound calls work from the start.
func New(cfg Config) (*Network, error) {
	cfg.setDefaults()
	if len(cfg.Key) != ed25519.PrivateKeySize {
		return nil, errors.New("network: the network key is not an Ed25519 private key")
	}
	if cfg.Clock == nil {
		return nil, errors.New("network: no clock")
	}
	acct, err := preauth.NewAccounting(cfg.Params)
	if err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	priv, err := ic.UnmarshalEd25519PrivateKey(cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("network: network key: %w", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("network: network key: %w", err)
	}

	n := &Network{
		cfg:         cfg,
		clock:       cfg.Clock,
		log:         cfg.Log,
		peer:        id,
		acct:        acct,
		conns:       map[lnet.Conn]*connEntry{},
		byKey:       map[preauth.ConnKey]*connEntry{},
		strangers:   map[ident.NodeID]record.SerialSeen{},
		heldKeys:    map[ident.PublicKey]struct{}{},
		book:        map[ident.NodeID][]ma.Multiaddr{},
		dialPeers:   map[ident.NodeID][]peer.ID{},
		bookWritten: map[peer.ID][]ma.Multiaddr{},
		unreachable: map[ident.NodeID]time.Time{},
		callStreaks: map[ident.NodeID]*callStreak{},
	}
	n.ctx, n.cancel = context.WithCancel(context.Background())
	n.limiter = newPeerLimiter(n.clock.Now)
	rm, err := newResourceManager(n.limiter)
	if err != nil {
		n.cancel()
		return nil, err
	}
	n.lib, err = newHost(priv, id, rm, &gater{n})
	if err != nil {
		rm.Close()
		n.cancel()
		return nil, err
	}
	n.lib.host.Network().Notify(&lnet.NotifyBundle{
		ConnectedF:    func(_ lnet.Network, c lnet.Conn) { n.connected(c) },
		DisconnectedF: func(_ lnet.Network, c lnet.Conn) { n.disconnected(c) },
	})
	n.registerHandlers()
	n.start(n.tickLoop)
	return n, nil
}

// SetNode gives the network the engine's side of the seam. It is called before
// Listen; a stream that arrives while no node is set is reset.
func (n *Network) SetNode(node Node) { n.node.Store(&nodeHolder{node}) }

func (n *Network) currentNode() Node {
	if h := n.node.Load(); h != nil {
		return h.node
	}
	return nil
}

// PeerID is the libp2p identifier of the node's network key, the one a contact
// line names after /p2p/.
func (n *Network) PeerID() string { return n.peer.String() }

// Listen opens the listeners. An address that cannot be opened (a port in use)
// fails the call with the address in the error.
func (n *Network) Listen(addrs []string) error {
	parsed := make([]ma.Multiaddr, 0, len(addrs))
	for _, a := range addrs {
		m, err := ma.NewMultiaddr(a)
		if err != nil {
			return fmt.Errorf("network: listen address %q: %w", a, err)
		}
		parsed = append(parsed, m)
	}
	for _, m := range parsed {
		if err := n.lib.host.Network().Listen(m); err != nil {
			return fmt.Errorf("network: listen on %s: %w", m, err)
		}
	}
	return nil
}

// ListenAddrs returns the addresses the listeners are open on, as multiaddr
// text with the port the system chose where the address asked for port 0.
func (n *Network) ListenAddrs() []string {
	addrs := n.lib.host.Network().ListenAddresses()
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// Close stops the listeners, closes every connection and waits for every
// goroutine the package started. It is safe to call more than once.
func (n *Network) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	n.mu.Unlock()

	n.cancel()
	err := n.lib.close()
	n.wg.Wait()
	return err
}

// begin registers a goroutine of the package with the wait group, or refuses
// after Close began, so that Close can wait for every one of them. The caller
// that got true calls n.wg.Done when the goroutine ends.
func (n *Network) begin() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return false
	}
	n.wg.Add(1)
	return true
}

// start runs f as a goroutine of the package, or not at all after Close began.
func (n *Network) start(f func()) bool {
	if !n.begin() {
		return false
	}
	go func() {
		defer n.wg.Done()
		f()
	}()
	return true
}
