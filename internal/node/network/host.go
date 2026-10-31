package network

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"fmt"

	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	"github.com/quic-go/quic-go"
)

// userAgent and protocolVersion are what identify tells a peer about this node.
const (
	userAgent       = "sidera"
	protocolVersion = "sidera/1"
)

// libp2pHost is the library host and the parts that need closing after it.
type libp2pHost struct {
	host *basic.BasicHost
	quic *quicreuse.ConnManager
}

// close stops the host, which closes the swarm, its transports and the resource
// manager, and then the QUIC connection manager the host does not own.
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
	return &libp2pHost{host: h, quic: quicConns}, nil
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
