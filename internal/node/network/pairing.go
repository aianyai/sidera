package network

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	lnet "github.com/libp2p/go-libp2p/core/network"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/transport"
	"github.com/aianyai/sidera/internal/nodeproto"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/preauth"
	"github.com/aianyai/sidera/internal/record"
)

// The network carries the pairing protocol and decides nothing about it: it
// opens and serves the streams, frames what the engine's state machine writes and
// hands over what it reads. A pairing is between nodes that need not be members
// of each other, so the facts of the connection it runs on (which certificate
// each end showed, and which key each end's connection is authenticated with) are
// the network's to give.

// pairingQueueLen is how many frames a stream holds for its writer. A target
// sends a couple of frames per stream, so a full queue means a writer that is not
// draining, and the stream is reset rather than the engine's loop made to wait.
const pairingQueueLen = 4

// PairingPeer is the facts of the connection a pairing stream or a result fetch
// ran on.
type PairingPeer struct {
	// Conn is the two parties as this connection shows them, and the protocol
	// the stream negotiated.
	Conn pairing.Conn
	// Cert is the remote node's certificate that preauth.Decide accepted on this
	// connection, and Record the signed record it was verified from, which is
	// what a credential stores.
	Cert   record.Verified[record.NetworkKeyCertificate]
	Record record.Record
	// Seen is the serial state of the remote node to store when it is higher
	// than the stored one.
	Seen record.SerialSeen
}

// PairingStream is the network's end of one pairing stream, which the engine
// writes to. Both methods may be called from the engine's loop: neither blocks.
type PairingStream interface {
	// Send queues one frame. A frame that cannot be framed, or one that finds
	// the queue full or the stream gone, is dropped, and the first two reset the
	// stream.
	Send(frame []byte)
	// Close ends the engine's side of the stream once the queued frames are
	// written: the stream is half-closed and kept, with its handler and the
	// connection it holds busy, until the peer ends its own side, so that the
	// last frame is not lost to a close of the connection that follows. Frames
	// the peer sends after Close are dropped, and the network does not report
	// an end the engine made.
	Close()
}

// pairingPeer builds the facts of a pairing on the connection. Each party is made
// from the certificate this connection accepted for it (the remote's by
// preauth.Decide, the node's own as its exchange presented it), the key this
// connection is authenticated with, and the current revocation records and
// highest serial seen: a certificate that is revoked, expired or older than one
// seen since is no party. proto is the protocol of the stream.
func (n *Network) pairingPeer(e *connEntry, proto string) (PairingPeer, error) {
	n.mu.Lock()
	facts, ok := n.factsLocked()
	state, cert, rec, seen, own := e.state, e.peerCert, e.peerRecord, e.peerSeen, e.ownCert
	n.mu.Unlock()
	if !ok || !mayPair(state) || cert.Digest() == (record.Digest{}) || own.Digest() == (record.Digest{}) {
		return PairingPeer{}, errors.New("the connection has no accepted certificates")
	}
	selfKey, err := publicKeyOf(n.peer)
	if err != nil {
		return PairingPeer{}, err
	}
	party := func(c record.Verified[record.NetworkKeyCertificate], key ident.PublicKey, known uint64) (pairing.Party, error) {
		node, serial := c.Payload().Node, c.Payload().Serial
		highest := max(serial, known, facts.SerialSeen[node].Serial)
		status := facts.Revocations.NetworkKey(c, highest).Revocation
		return pairing.NewParty(c, status, highest, key, facts.Now)
	}
	remote, err := party(cert, e.peerKey, seen.Serial)
	if err != nil {
		return PairingPeer{}, fmt.Errorf("the remote node: %w", err)
	}
	local, err := party(own, selfKey, 0)
	if err != nil {
		return PairingPeer{}, fmt.Errorf("this node: %w", err)
	}
	return PairingPeer{
		Conn:   pairing.Conn{Local: local, Remote: remote, Protocol: proto},
		Cert:   cert,
		Record: rec,
		Seen:   seen,
	}, nil
}

// pairingStream is the network's end of a pairing stream. Frames the engine sends
// are queued and written by the stream's own goroutine; the stream is read by
// the goroutine that owns it.
type pairingStream struct {
	n    *Network
	s    lnet.Stream
	life context.Context // ends at the online timeout; the stream is reset then
	// queue holds framed bytes for the writer, which ends when it is closed and
	// drained. done is closed when the writer has ended.
	queue chan []byte
	done  chan struct{}

	mu sync.Mutex
	// ended means the queue is closed and no frame is taken any more.
	ended bool
	// byEngine means the engine's Close ended it, so its end is not reported.
	byEngine bool
}

func (n *Network) newPairingStream(s lnet.Stream, life context.Context) *pairingStream {
	return &pairingStream{n: n, s: s, life: life, queue: make(chan []byte, pairingQueueLen), done: make(chan struct{})}
}

// startWriter starts the writer goroutine; after Close of the network it
// resets the stream and says no.
func (p *pairingStream) startWriter() bool {
	if !p.n.start(p.write) {
		p.s.Reset()
		return false
	}
	return true
}

// write writes the queued frames in order. When the queue is closed and drained
// it half-closes the stream: the peer reads what was written and then the end,
// and what it sends is still read here, so that the stream stays until the peer
// has ended its side (see finish). A write that fails resets the stream.
func (p *pairingStream) write() {
	defer close(p.done)
	for frame := range p.queue {
		if _, err := p.s.Write(frame); err != nil {
			p.n.log.Debug("a pairing frame could not be written", "peer", p.s.Conn().RemotePeer(), "err", err)
			p.end()
			p.s.Reset()
			return
		}
	}
	p.s.CloseWrite()
}

// finish waits for the writer, then closes the stream. It is called once the
// read has returned, which is when the peer has ended its side, or the stream
// was reset. A handler that returned any earlier would release its connection
// while the peer had perhaps not yet taken the last frame, and a close of an idle
// connection discards what is still in flight.
func (p *pairingStream) finish() {
	<-p.done
	p.s.Close()
}

// end stops taking frames: the writer ends once it has written what is queued.
func (p *pairingStream) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ended {
		p.ended = true
		close(p.queue)
	}
}

// reset ends the stream at once, dropping what is queued.
func (p *pairingStream) reset() {
	p.end()
	p.s.Reset()
}

// Send implements PairingStream.
func (p *pairingStream) Send(frame []byte) {
	framed, err := nodeproto.AppendFrame(nil, frame, pairing.MaxMessage)
	if err != nil {
		p.n.log.Warn("a pairing frame cannot be framed; the stream is reset", "peer", p.s.Conn().RemotePeer(), "err", err)
		p.reset()
		return
	}
	p.mu.Lock()
	switch {
	case p.ended:
		p.mu.Unlock()
		p.n.log.Debug("a pairing frame is dropped: the stream has ended", "peer", p.s.Conn().RemotePeer())
	default:
		select {
		case p.queue <- framed:
			p.mu.Unlock()
		default:
			p.mu.Unlock()
			p.n.log.Warn("a pairing stream's queue is full; the stream is reset", "peer", p.s.Conn().RemotePeer())
			p.reset()
		}
	}
}

// Close implements PairingStream.
func (p *pairingStream) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ended {
		p.byEngine = true
		p.ended = true
		close(p.queue)
	}
}

// peerEnded settles the end of the stream the network saw, cleanly (the peer
// closed its write side) or not, and reports whether the engine is to be told.
// A clean end leaves the writer to flush what is queued; any other end resets.
func (p *pairingStream) peerEnded(clean bool) (notify bool) {
	p.mu.Lock()
	notify = !p.byEngine
	p.mu.Unlock()
	if clean {
		p.end()
	} else {
		p.reset()
	}
	return notify
}

// read hands the frames of the stream to the engine until the peer ends its
// side, then tells the engine if the end was not its own. The engine hears of
// every frame before the next is read. After the engine's Close the stream is
// still read, to see the peer's end, but its frames are dropped.
func (p *pairingStream) read(node Node, sid pairing.SessionID) {
	r := bufio.NewReader(p.s)
	clean := false
	for {
		frame, err := nodeproto.ReadFrame(r, pairing.MaxMessage)
		if err != nil {
			clean = err == io.EOF
			break
		}
		p.mu.Lock()
		byEngine := p.byEngine
		p.mu.Unlock()
		if byEngine {
			continue
		}
		if err := node.PairingFrame(p.life, sid, frame); err != nil {
			p.n.log.Debug("the engine took no more pairing frames", "peer", p.s.Conn().RemotePeer(), "err", err)
			break
		}
	}
	if p.peerEnded(clean) {
		node.PairingClosed(p.n.ctx, sid)
	}
}

// servePairing is the target's end of a pairing stream, of its online segment or
// of a result fetch. The stream ends at the online timeout at the latest.
func (n *Network) servePairing(ctx context.Context, s lnet.Stream, e *connEntry) {
	node := n.currentNode()
	if node == nil {
		s.Reset()
		return
	}
	peer, err := n.pairingPeer(e, string(s.Protocol()))
	if err != nil {
		n.log.Debug("a pairing stream is refused", "peer", e.conn.RemotePeer(), "err", err)
		s.Reset()
		return
	}
	life, cancel := context.WithTimeout(ctx, n.cfg.Params.PairingOnlineTimeout)
	defer cancel()
	stop := context.AfterFunc(life, func() { s.Reset() })
	defer stop()

	ps := n.newPairingStream(s, life)
	if !ps.startWriter() {
		return
	}
	sid, err := node.PairingOpened(life, peer, ps)
	if err != nil {
		n.log.Debug("the engine refused a pairing stream", "peer", e.conn.RemotePeer(), "err", err)
		ps.reset()
		ps.finish()
		return
	}
	ps.read(node, sid)
	ps.finish()
}

// openPairingStream opens a pairing stream on the connection, within the connect
// budget, and takes the facts of the pairing from the stream and the connection.
// Nothing has been written when it returns. The errors are those of a call: the
// caller's own context error, or transport.ErrUnreachable when the stream could
// not be opened, which the peer cannot have seen.
func (n *Network) openPairingStream(ctx context.Context, e *connEntry) (lnet.Stream, PairingPeer, error) {
	octx, cancel := context.WithTimeout(ctx, n.cfg.ConnectTimeout)
	defer cancel()
	s, err := n.openStream(octx, e, string(preauth.ProtocolPairing))
	if err != nil {
		if ctx.Err() != nil {
			return nil, PairingPeer{}, ctx.Err()
		}
		return nil, PairingPeer{}, fmt.Errorf("%w: %v", transport.ErrUnreachable, err)
	}
	peer, err := n.pairingPeer(e, string(s.Protocol()))
	if err != nil {
		s.Reset()
		return nil, PairingPeer{}, fmt.Errorf("network: pairing with %s: %w", e.node, err)
	}
	return s, peer, nil
}

// StartPairing opens the initiator's end of a pairing stream to the node and
// returns the facts of the connection and the stream the engine writes to. The
// frames the node sends are handed to the engine as frames of session sid, and
// the end of the stream, when it is not the engine's own, as the close of sid;
// both may arrive before StartPairing returns, so the engine takes a close of a
// session it has not registered yet as a stream that is already over.
//
// The node is dialed on addrs when there are any, which are hints for this
// pairing only and are not kept; otherwise on the address book's. The connection
// must reach exactly this node, as a member or a non-member. The stream ends at
// the online timeout at the latest. Errors are those of a call: the caller's own
// context error, or transport.ErrUnreachable when the node could not be connected
// or the stream not opened.
func (n *Network) StartPairing(ctx context.Context, node ident.NodeID, addrs []string, sid pairing.SessionID) (PairingPeer, PairingStream, error) {
	engine := n.currentNode()
	if engine == nil {
		return PairingPeer{}, nil, errors.New("network: no node is set")
	}
	e, err := n.pairingConn(ctx, node, addrs)
	if err != nil {
		return PairingPeer{}, nil, err
	}
	if !n.acquire(e) {
		return PairingPeer{}, nil, fmt.Errorf("%w: the connection ended", transport.ErrUnreachable)
	}
	s, peer, err := n.openPairingStream(ctx, e)
	if err != nil {
		n.release(e)
		return PairingPeer{}, nil, err
	}
	life, cancel := context.WithTimeout(n.ctx, n.cfg.Params.PairingOnlineTimeout)
	ps := n.newPairingStream(s, life)
	if !ps.startWriter() {
		cancel()
		n.release(e)
		return PairingPeer{}, nil, errors.New("network: closed")
	}
	started := n.start(func() {
		defer n.release(e)
		defer cancel()
		stop := context.AfterFunc(life, func() { s.Reset() })
		defer stop()
		ps.read(engine, sid)
		ps.finish()
	})
	if !started {
		ps.reset()
		ps.finish()
		cancel()
		n.release(e)
		return PairingPeer{}, nil, errors.New("network: closed")
	}
	return peer, ps, nil
}

// PairingFetch asks the node for the result of a pairing in one exchange: the
// frame goes out on a pairing stream and the one frame that answers comes back,
// within the fetch timeout from the opening of the stream. The stream is not
// half-closed after the frame: the target's handler lives until it sees the end
// of the stream, so the end it sees is the close that follows the answer, which
// tells it the answer was taken. The connection is one that is up to the node (a member's or a
// non-member's) or a new one, made as StartPairing makes it. It returns the
// answer and the facts of the connection it came over. Errors are those of a
// call: the caller's own context error, transport.ErrUnreachable when the node
// could not be connected or the stream was not seen, and transport.ErrTimeout
// when the frame went out and no answer came.
func (n *Network) PairingFetch(ctx context.Context, node ident.NodeID, addrs []string, frame []byte) ([]byte, PairingPeer, error) {
	framed, err := nodeproto.AppendFrame(nil, frame, pairing.MaxMessage)
	if err != nil {
		return nil, PairingPeer{}, fmt.Errorf("network: pairing fetch: %w", err)
	}
	e, err := n.pairingConn(ctx, node, addrs)
	if err != nil {
		return nil, PairingPeer{}, err
	}
	if !n.acquire(e) {
		return nil, PairingPeer{}, fmt.Errorf("%w: the connection ended", transport.ErrUnreachable)
	}
	defer n.release(e)
	s, peer, err := n.openPairingStream(ctx, e)
	if err != nil {
		return nil, PairingPeer{}, err
	}

	fctx, cancel := context.WithTimeout(ctx, n.cfg.FetchTimeout)
	defer cancel()
	stop := context.AfterFunc(fctx, func() { s.Reset() })
	defer stop()
	// As in a call, a failed write is not judged by itself: the read that follows
	// says why the stream is dead.
	_, _ = s.Write(framed)
	answer, err := nodeproto.ReadFrame(bufio.NewReader(s), pairing.MaxMessage)
	if err != nil {
		return nil, PairingPeer{}, noAnswer(ctx, s, err)
	}
	s.Close()
	return answer, peer, nil
}
