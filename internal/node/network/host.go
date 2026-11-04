package network

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"
	"net"
	"sync"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/sec"
	basic "github.com/libp2p/go-libp2p/p2p/host/basic"
	"github.com/libp2p/go-libp2p/p2p/host/eventbus"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoremem"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/quic-go/quic-go"
)

// userAgent and protocolVersion are what identify tells a peer about this node.
const (
	userAgent       = "sidera"
	protocolVersion = "sidera/1"
)

// libp2pHost is the library host and the parts that need closing after it.
type libp2pHost struct {
	host     *basic.BasicHost
	quic     *quicreuse.ConnManager
	dialBack *dialBackDialer
}

// close stops the host, which closes the swarm, its transports and the resource
// manager, then the QUIC connection manager the host does not own.
func (h *libp2pHost) close() error {
	err := h.host.Close()
	if qerr := h.quic.Close(); err == nil {
		err = qerr
	}
	return err
}

// newHost assembles the library host from its sub-packages instead of calling
// libp2p.New: swarm, upgrader, TCP with Noise, QUIC with its own TLS handshake,
// yamux, and the basic host with identify and ping. The root package would link
// WebRTC, WebSocket and WebTransport whatever options it is given. The NAT
// manager, the relay service, hole punching and autonat stay off: nothing here
// opens a router port or reaches a node through a relay.
//
// The host also holds the dial-back dialer, which makes a swarm that listens on
// nothing for every dial-back (see dialBackDialer).
//
// The resource manager and the connection gater are the caller's, because both
// are driven by the connection table.
func newHost(priv ic.PrivKey, id peer.ID, rm lnet.ResourceManager, gater connmgr.ConnectionGater) (_ *libp2pHost, err error) {
	var closers []func()
	defer func() {
		if err != nil {
			for i := len(closers) - 1; i >= 0; i-- {
				closers[i]()
			}
		}
	}()

	ps, err := pstoremem.NewPeerstore()
	if err != nil {
		return nil, fmt.Errorf("network: peerstore: %w", err)
	}
	closers = append(closers, func() { ps.Close() })
	if err := ps.AddPrivKey(id, priv); err != nil {
		return nil, fmt.Errorf("network: peerstore key: %w", err)
	}
	if err := ps.AddPubKey(id, priv.GetPublic()); err != nil {
		return nil, fmt.Errorf("network: peerstore key: %w", err)
	}

	muxers := []upgrader.StreamMuxer{{ID: yamux.ID, Muxer: yamux.DefaultTransport}}
	noiseTransport, err := noise.New(noise.ID, priv, muxers)
	if err != nil {
		return nil, fmt.Errorf("network: noise: %w", err)
	}
	up, err := upgrader.New([]sec.SecureTransport{noiseTransport}, muxers, nil, rm, gater)
	if err != nil {
		return nil, fmt.Errorf("network: upgrader: %w", err)
	}

	bus := eventbus.NewBus()
	sw, err := swarm.NewSwarm(id, ps, bus, swarm.WithConnectionGater(gater), swarm.WithResourceManager(rm))
	if err != nil {
		return nil, fmt.Errorf("network: swarm: %w", err)
	}
	closers = append(closers, func() { sw.Close() })

	tcpTransport, err := tcp.NewTCPTransport(up, rm, nil)
	if err != nil {
		return nil, fmt.Errorf("network: tcp: %w", err)
	}
	if err := sw.AddTransport(tcpTransport); err != nil {
		return nil, fmt.Errorf("network: tcp: %w", err)
	}

	resetKey, tokenKey, err := quicKeys(priv)
	if err != nil {
		return nil, err
	}
	quicConns, err := quicreuse.NewConnManager(resetKey, tokenKey,
		// A QUIC connection is accepted before the swarm sees it, so its
		// resource scope is opened here, as the library's own assembly does.
		quicreuse.ConnContext(func(ctx context.Context, info *quic.ClientInfo) (context.Context, error) {
			addr, err := quicreuse.ToQuicMultiaddr(info.RemoteAddr, quic.Version1)
			if err != nil {
				addr = nil // let the resource manager decide on a connection it cannot place
			}
			scope, err := rm.OpenConnection(lnet.DirInbound, false, addr)
			if err != nil {
				return ctx, err
			}
			ctx = lnet.WithConnManagementScope(ctx, scope)
			context.AfterFunc(ctx, scope.Done)
			return ctx, nil
		}),
		quicreuse.VerifySourceAddress(rm.VerifySourceAddress),
	)
	if err != nil {
		return nil, fmt.Errorf("network: quic connections: %w", err)
	}
	closers = append(closers, func() { quicConns.Close() })
	quicTransport, err := libp2pquic.NewTransport(priv, quicConns, nil, gater, rm)
	if err != nil {
		return nil, fmt.Errorf("network: quic: %w", err)
	}
	if err := sw.AddTransport(quicTransport); err != nil {
		return nil, fmt.Errorf("network: quic: %w", err)
	}

	dialBack, err := newDialBackDialer(priv, id)
	if err != nil {
		return nil, err
	}

	h, err := basic.NewHost(sw, &basic.HostOpts{
		EventBus:        bus,
		EnablePing:      true,
		UserAgent:       userAgent,
		ProtocolVersion: protocolVersion,
	})
	if err != nil {
		return nil, fmt.Errorf("network: host: %w", err)
	}
	h.Start()
	return &libp2pHost{host: h, quic: quicConns, dialBack: dialBack}, nil
}

// quicKeys derives the two secrets of the QUIC connection manager from the
// network key, the way the library's own assembly does, so that a restart keeps
// stateless resets and address-validation tokens valid.
func quicKeys(priv ic.PrivKey) (reset quic.StatelessResetKey, token quic.TokenGeneratorKey, err error) {
	raw, err := priv.Raw()
	if err != nil {
		return reset, token, fmt.Errorf("network: quic keys: %w", err)
	}
	for _, k := range []struct {
		info string
		out  []byte
	}{
		{"libp2p quic stateless reset key", reset[:]},
		{"libp2p quic token generator key", token[:]},
	} {
		derived, err := hkdf.Key(sha256.New, raw, nil, k.info, len(k.out))
		if err != nil {
			return reset, token, fmt.Errorf("network: quic keys: %w", err)
		}
		copy(k.out, derived)
	}
	return reset, token, nil
}

// dialBackDialer makes the connections that answer "can you dial me back?". Every
// dial-back is made by a swarm of its own that listens on nothing, built for the
// dial and closed with it, because the answer must come from a connection that is
// new in every sense a firewall or a NAT sees:
//
//   - The host's own swarm would return the connection it already has to the
//     peer, and does not dial at all; and a new connection of the host's own
//     leaves the listening port (TCP reuses it for dialing, and QUIC shares the
//     listening socket), so its packets are the very flow the asker's earlier
//     connection opened, which a stateful firewall passes while it blocks a
//     cold connection. So its TCP transport does not reuse ports, and its QUIC
//     connection manager is its own and does not either: every dial leaves from
//     a new port.
//   - It is authenticated with the node's own network key, so the peer, which
//     asked, sees a member it knows, which the accounting admits without a place
//     among the unauthenticated connections.
//   - Its dial is not delayed by address ranking (every address of the peer is
//     tried at once and the first that connects is the answer, as a call's dial
//     takes the first address that connects), and it keeps no black-hole state
//     for UDP and IPv6: after a few failed dials the library would stop trying
//     such addresses, and the dialer would answer "not reached" for them without
//     having tried.
//   - It has no resource manager and no gater: the question's own bounds (the
//     concurrency and the connect budget) are the limits of what it does, and
//     its connection is closed as soon as it is up.
//
// The swarm is not kept between dials because a QUIC connection manager that
// does not reuse sockets opens a UDP socket for every dial and never closes it:
// the library gives up the transport of a dial that succeeded only when the
// manager is closed, and closing the manager does nothing when reuse is off. A
// swarm of its own per dial can be closed together with every socket it opened.
// Dial-backs are rare and bounded (see maxDialBacks), so building one costs
// nothing that matters.
type dialBackDialer struct {
	priv ic.PrivKey
	id   peer.ID
	// resetKey and tokenKey are the secrets of the QUIC connection manager of
	// every dial.
	resetKey quic.StatelessResetKey
	tokenKey quic.TokenGeneratorKey
}

func newDialBackDialer(priv ic.PrivKey, id peer.ID) (*dialBackDialer, error) {
	resetKey, tokenKey, err := quicKeys(priv)
	if err != nil {
		return nil, err
	}
	return &dialBackDialer{priv: priv, id: id, resetKey: resetKey, tokenKey: tokenKey}, nil
}

// dialBackConn is the connection a dial-back made, by its two ends.
type dialBackConn struct {
	// Remote is the address of the peer that connected.
	Remote ma.Multiaddr
	// Local is the address the connection left from.
	Local ma.Multiaddr
}

// dial connects to the peer on the addresses, completing the secure handshake
// and the stream-muxer negotiation, and closes the connection at once. It returns
// the address that connected and the one it left from. The addresses are the
// call's: nothing of the dial outlives it. Calls may overlap.
func (d *dialBackDialer) dial(ctx context.Context, pid peer.ID, addrs []ma.Multiaddr) (dialBackConn, error) {
	s, err := d.open()
	if err != nil {
		return dialBackConn{}, err
	}
	defer s.close()
	s.peers.AddAddrs(pid, addrs, peerstore.TempAddrTTL)
	// The library's own dial backoff would answer "not reached" for a peer that
	// was dialed and failed a moment ago, whatever has changed since.
	ctx = lnet.WithForceDirectDial(ctx, "a dial-back is a fresh attempt every time")
	c, err := s.swarm.DialPeer(ctx, pid)
	if err != nil {
		return dialBackConn{}, err
	}
	return dialBackConn{Remote: c.RemoteMultiaddr(), Local: c.LocalMultiaddr()}, nil
}

// dialBackSwarm is the swarm of one dial-back and everything that has to be closed
// with it.
type dialBackSwarm struct {
	swarm *swarm.Swarm
	peers peerstore.Peerstore
	quic  *quicreuse.ConnManager

	mu      sync.Mutex
	sockets []net.PacketConn
}

// open builds the swarm of one dial-back.
func (d *dialBackDialer) open() (_ *dialBackSwarm, err error) {
	s := &dialBackSwarm{}
	defer func() {
		if err != nil {
			s.close()
		}
	}()

	s.peers, err = pstoremem.NewPeerstore()
	if err != nil {
		return nil, fmt.Errorf("network: dial-back peerstore: %w", err)
	}

	muxers := []upgrader.StreamMuxer{{ID: yamux.ID, Muxer: yamux.DefaultTransport}}
	noiseTransport, err := noise.New(noise.ID, d.priv, muxers)
	if err != nil {
		return nil, fmt.Errorf("network: dial-back noise: %w", err)
	}
	up, err := upgrader.New([]sec.SecureTransport{noiseTransport}, muxers, nil, &lnet.NullResourceManager{}, nil)
	if err != nil {
		return nil, fmt.Errorf("network: dial-back upgrader: %w", err)
	}
	s.swarm, err = swarm.NewSwarm(d.id, s.peers, eventbus.NewBus(),
		swarm.WithDialRanker(swarm.NoDelayDialRanker),
		swarm.WithUDPBlackHoleSuccessCounter(nil),
		swarm.WithIPv6BlackHoleSuccessCounter(nil),
	)
	if err != nil {
		return nil, fmt.Errorf("network: dial-back swarm: %w", err)
	}

	tcpTransport, err := tcp.NewTCPTransport(up, &lnet.NullResourceManager{}, nil, tcp.DisableReuseport())
	if err != nil {
		return nil, fmt.Errorf("network: dial-back tcp: %w", err)
	}
	if err := s.swarm.AddTransport(tcpTransport); err != nil {
		return nil, fmt.Errorf("network: dial-back tcp: %w", err)
	}

	s.quic, err = quicreuse.NewConnManager(d.resetKey, d.tokenKey, quicreuse.DisableReuseport(),
		quicreuse.OverrideListenUDP(func(network string, laddr *net.UDPAddr) (net.PacketConn, error) {
			conn, err := net.ListenUDP(network, laddr)
			if err != nil {
				return nil, err
			}
			s.mu.Lock()
			s.sockets = append(s.sockets, conn)
			s.mu.Unlock()
			return conn, nil
		}))
	if err != nil {
		return nil, fmt.Errorf("network: dial-back quic connections: %w", err)
	}
	quicTransport, err := libp2pquic.NewTransport(d.priv, s.quic, nil, nil, &lnet.NullResourceManager{})
	if err != nil {
		return nil, fmt.Errorf("network: dial-back quic: %w", err)
	}
	if err := s.swarm.AddTransport(quicTransport); err != nil {
		return nil, fmt.Errorf("network: dial-back quic: %w", err)
	}
	return s, nil
}

// close closes the swarm, which closes its transports and connections, then what
// the swarm does not own: the QUIC connection manager, every UDP socket it opened
// (the manager closes none of them when it does not reuse sockets) and the
// peerstore. Parts that were not built are skipped.
func (s *dialBackSwarm) close() {
	if s.swarm != nil {
		s.swarm.Close()
	}
	if s.quic != nil {
		s.quic.Close()
	}
	s.mu.Lock()
	for _, conn := range s.sockets {
		conn.Close()
	}
	s.mu.Unlock()
	if s.peers != nil {
		s.peers.Close()
	}
}
