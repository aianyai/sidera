package engine

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/pairing"
	"github.com/aianyai/sidera/internal/record"
)

// The engine is the caller `internal/pairing` describes: the state machine is a
// value the loop keeps, every call returns its next value and the actions to carry
// out, and this file carries them out. The network opens and reads the streams and
// the engine holds the sessions they run on; neither imports the other, so the
// facts of a connection and the stream the engine writes to are mirrored here and
// the composition root converts.

// pairingTickEvery is how often the loop runs the state machine's Tick while
// something of a pairing is under way: a stream is open, a request waits for its
// owner, or an initiator's request has not ended. Commands and events tick it as
// well, so this is only the pace at which time alone makes things happen.
const pairingTickEvery = time.Second

// PairingPeer is the facts of the connection a pairing stream or a result fetch
// ran on. It has the fields of the network layer's own type and no more, so that
// the one converts to the other as it is.
type PairingPeer struct {
	// Conn is the two parties as the connection shows them, and the protocol the
	// stream negotiated.
	Conn pairing.Conn
	// Cert is the remote node's certificate that the connection accepted, and
	// Record the signed record it was verified from.
	Cert   record.Verified[record.NetworkKeyCertificate]
	Record record.Record
	// Seen is the serial state of the remote node to keep when it is above the one
	// stored.
	Seen record.SerialSeen
}

// certificate is the accepted certificate of the connection, as the trust records
// take it with a credential.
func (p PairingPeer) certificate() *peerCertificate {
	return &peerCertificate{cert: p.Cert, rec: p.Record, seen: p.Seen}
}

// PairingStream is the network's end of one pairing stream, which the engine
// writes to. Neither method blocks, so the loop may call them.
type PairingStream interface {
	// Send queues one frame.
	Send(frame []byte)
	// Close ends the stream once the queued frames are written. The network does
	// not report an end the engine made.
	Close()
}

// pairingSide is which end of a pairing a session is.
type pairingSide uint8

const (
	sideTarget pairingSide = iota + 1
	sideInitiator
)

// pairingSession is a stream the engine holds for the state machine.
type pairingSession struct {
	side   pairingSide
	stream PairingStream
	peer   PairingPeer
}

// pairingFetch is a result exchange the state machine asked for, from the moment
// it is asked for until its answer is taken in.
type pairingFetch struct {
	req  pairing.FetchRequest
	node ident.NodeID
}

// pairingDriver is what the loop keeps for the pairing: the state machine's value
// and the sessions and fetches that belong to the node's own process.
type pairingDriver struct {
	state    pairing.State
	sessions map[pairing.SessionID]*pairingSession
	last     pairing.SessionID // the identifier of the latest session; they are never reused
	// fetches are the exchanges asked for and not answered yet, held or running, so
	// that one pairing never has two. held are those not handed to the network yet,
	// in the order they were asked for.
	fetches map[pairing.ID]*pairingFetch
	held    []*pairingFetch
	// fetchesOpen says the network may be asked: the second recovery entry has
	// ended. Until then a node that is starting has no connection a fetch could use.
	fetchesOpen bool
	// requests are the owner's requests for a pairing that are not answered yet; see
	// pairing_request.go.
	requests map[admin.RequestKey]*pairingRequestInFlight
	// recoverDue says the state machine's Recover has not run since the node
	// started, because the node might not sign then; the first tick that may sign
	// runs it.
	recoverDue bool
}

// openPairing builds the state machine from the scan and recovers it. A stored
// state that does not restore is damaged beyond what the engine can run with.
func (e *Engine) openPairing(sc *store.Scan) error {
	st, err := pairing.Restore(pairing.Snapshot{
		Limits: sc.PairingLimits, Expectations: sc.PairingExpectations,
		Targets: sc.PairingTargets, Initiators: sc.PairingInitiators,
	})
	if err != nil {
		return fmt.Errorf("engine: the stored pairing state cannot be restored: %w", err)
	}
	e.pairing = &pairingDriver{
		state:    st,
		sessions: map[pairing.SessionID]*pairingSession{},
		fetches:  map[pairing.ID]*pairingFetch{},
		requests: map[admin.RequestKey]*pairingRequestInFlight{},
	}
	if e.d.Keys == nil {
		return nil // no identity key to sign with: the node runs no pairing
	}
	e.pairing.recoverDue = true
	if e.signRefusal() != 0 {
		return nil // Recover may sign: it runs at the first tick that may
	}
	return e.recoverPairing(e.d.Clock.Now())
}

// recoverPairing runs the state machine's Recover and carries out what it asks for.
// It signs (it stores the revocation of a signature that may have left the node),
// so it is called only while the node may sign.
func (e *Engine) recoverPairing(now time.Time) error {
	next, acts, err := e.pairing.state.Recover(e.pairingWorld(), now)
	if err != nil {
		return fmt.Errorf("engine: recovering the pairing state: %w", err)
	}
	if err := e.pairingPerform(next, acts, nil); err != nil {
		return err
	}
	e.pairing.recoverDue = false
	return nil
}

// pairingWorld is what the state machine reads of the node, made fresh for every
// call. It holds the identity signer whenever the node has keys, so the engine, not
// the world, keeps the node from signing while signRefusal says it may not: every
// entry that can make the state machine sign (a request for a pairing, the rejection
// of one, an incoming stream and its frames, a result fetch, a tick, the recovery)
// asks canSign or signRefusal before it calls the state machine. No authorized persons and no actors are given: the management interface is
// the local owner's alone, whom no list judges.
//
// Stored is not set, on purpose. The replay check of a management request has one
// home, the engine's table of stored results that Execute reads, and it is made
// before any command of the state machine is called. The state machine's own
// request fingerprint is not the one the management interface makes, so a second
// check against the same results would take a resend that the interface already
// judged to be one (the same request, to be answered with the stored result) for a
// reuse of its identifier. The state machine's authorization and validation of a
// request stay as they are: they are its guard, not a replay check.
func (e *Engine) pairingWorld() pairing.World {
	w := pairing.World{
		Params:      pairing.DefaultParams(),
		Revocations: e.d.Config.RevocationSet(),
		Trust:       e.trustView(),
		List:        admin.List{},
	}
	if e.d.Keys != nil {
		w.Identity = e.d.Keys.IdentitySigner()
	}
	return w
}

// ---------------------------------------------------------------------------
// Carrying out the actions

// pairingActionKind says whether an action changes what is durable or reaches
// something outside.
type pairingActionKind uint8

const (
	// actionPersist changes what is durable: it is carried out before the state is
	// taken, in the order of the list, and its failure ends the call.
	actionPersist pairingActionKind = iota + 1
	// actionIO reaches something outside: it is carried out after the state is taken.
	actionIO
)

func pairingActionKindOf(a pairing.Action) (pairingActionKind, error) {
	switch a.(type) {
	case pairing.PersistTarget, pairing.PersistInitiator, pairing.PersistLimits, pairing.PersistExpectations,
		pairing.DeleteTarget, pairing.DeleteInitiator, pairing.StoreCredential, pairing.StoreRevocation:
		return actionPersist, nil
	case pairing.Send, pairing.Close, pairing.FetchRequest, pairing.Notify:
		return actionIO, nil
	}
	return 0, fmt.Errorf("engine: the pairing returned an action of a kind the engine does not know: %T", a)
}

// pairingPerform carries out what a call of the state machine returned, and is the
// one place that decides in what order. The persists run first, in the order of the
// list, each its own write; only when all of them are durable is the next state
// taken and are the frames sent, the streams closed, the fetches asked for and the
// notices told, in the order of the list. A frame follows the persist that made it
// safe to send, and the state never runs ahead of the disk.
//
// The state machine lets a list interleave the two (a tick that expires two
// requests asks for a persist and a notice for each), and the I/O actions wait for
// every persist of the list, not only the ones before them. That is safe because no
// persist records that an earlier I/O action was done: a persist that a frame, a
// close or a fetch must follow always comes before it in the list, and what a
// persist keeps does not depend on a notice or a frame having gone out. A list is
// refused whole, before anything is done, only when it holds an action the engine
// does not know.
//
// peer is the accepted certificate of the connection the call ran on, which a
// credential is stored with; nil when the call ran on none.
func (e *Engine) pairingPerform(next pairing.State, acts []pairing.Action, peer *peerCertificate) error {
	for _, a := range acts {
		if _, err := pairingActionKindOf(a); err != nil {
			e.log.Error("a list of pairing actions is refused; nothing was done", "error", err.Error())
			return err
		}
	}
	// An owner's request for a pairing that this list ends has its result stored in
	// the transaction that saves the request, so that neither exists without the
	// other; see pairing_request.go.
	ends := e.endingRequests(acts)
	for _, a := range acts {
		if kind, _ := pairingActionKindOf(a); kind != actionPersist {
			continue
		}
		var err error
		if p, ok := a.(pairing.PersistInitiator); ok && savingRequest(ends, p.Record) != nil {
			err = e.persistRequested(savingRequest(ends, p.Record))
		} else {
			err = e.pairingPersist(a, peer)
		}
		if err != nil {
			return e.classify(err)
		}
	}
	e.pairing.state = next
	for _, a := range acts {
		if kind, _ := pairingActionKindOf(a); kind == actionIO {
			e.pairingIO(a)
		}
	}
	for _, end := range ends {
		if end.saved == nil {
			e.endUnsaved(end)
		}
	}
	return nil
}

// pairingPersist makes one persist action durable.
func (e *Engine) pairingPersist(a pairing.Action, peer *peerCertificate) error {
	switch a := a.(type) {
	case pairing.PersistTarget:
		return e.d.Store.PairingTargetPut(a.Record)
	case pairing.PersistInitiator:
		return e.d.Store.PairingInitiatorPut(a.Record)
	case pairing.PersistLimits:
		return e.d.Store.PairingLimitsPut(a.Limits)
	case pairing.PersistExpectations:
		return e.d.Store.PairingExpectationsPut(a.Expectations)
	case pairing.DeleteTarget:
		return e.d.Store.PairingTargetDelete(a.ID)
	case pairing.DeleteInitiator:
		return e.d.Store.PairingInitiatorDelete(a.ID)
	case pairing.StoreCredential:
		return e.installCredential(a.Record, peer)
	case pairing.StoreRevocation:
		return e.installRevocation(a.Record)
	}
	panic(fmt.Sprintf("engine: pairingPersist of %T", a)) // pairingActionKindOf did not say persist
}

// pairingIO carries out one action that reaches outside. A session the engine no
// longer holds is one that ended meanwhile: what was meant for it has nowhere to
// go.
func (e *Engine) pairingIO(a pairing.Action) {
	switch a := a.(type) {
	case pairing.Send:
		if s := e.pairing.sessions[a.Session]; s != nil {
			s.stream.Send(a.Frame)
		} else {
			e.log.Debug("a pairing frame is dropped: the session is gone", "session", uint64(a.Session))
		}
	case pairing.Close:
		// The state machine forgets a stream it closes, so nothing is told back.
		if s := e.pairing.sessions[a.Session]; s != nil {
			s.stream.Close()
			delete(e.pairing.sessions, a.Session)
		} else {
			e.log.Debug("a pairing close is dropped: the session is gone", "session", uint64(a.Session))
		}
	case pairing.FetchRequest:
		e.pairingFetchRequested(a)
	case pairing.Notify:
		n := a.Notice
		attrs := []any{"kind", noticeName(n.Kind), "remote", n.Remote.String()}
		if !n.ID.IsZero() {
			attrs = append(attrs, "pairing", hex.EncodeToString(n.ID[:]))
		}
		e.log.Info("pairing notice", attrs...)
	default:
		panic(fmt.Sprintf("engine: pairingIO of %T", a)) // pairingActionKindOf did not say I/O
	}
}

// noticeName is the name of a kind of notice, for the log.
func noticeName(k pairing.NoticeKind) string {
	switch k {
	case pairing.NoticeNotStarted:
		return "not-started"
	case pairing.NoticeAttemptUsed:
		return "attempt-used"
	case pairing.NoticeCodeReady:
		return "code-ready"
	case pairing.NoticePending:
		return "pending"
	case pairing.NoticeCompleted:
		return "completed"
	case pairing.NoticeEnded:
		return "ended"
	case pairing.NoticeRevoked:
		return "revoked"
	}
	return fmt.Sprintf("unknown(%d)", int(k))
}

// ---------------------------------------------------------------------------
// The result fetches

// pairingFetchRequested takes a request for a result exchange. It is held, not
// handed to the network, until the network can be used: from the scan entry to the
// end of the second recovery entry, and always when there is no network. A pairing
// whose exchange is already asked for is not asked for again.
func (e *Engine) pairingFetchRequested(req pairing.FetchRequest) {
	if _, asked := e.pairing.fetches[req.ID]; asked {
		e.log.Debug("a pairing fetch is not asked for twice", "pairing", hex.EncodeToString(req.ID[:]))
		return
	}
	rec, ok := e.pairing.state.Initiator(req.ID)
	if !ok {
		e.log.Error("a pairing fetch is asked for a request this node did not start", "pairing", hex.EncodeToString(req.ID[:]))
		return
	}
	f := &pairingFetch{req: req, node: rec.Transcript.Terms().Target.Node()}
	e.pairing.fetches[req.ID] = f
	e.pairing.held = append(e.pairing.held, f)
	e.startPairingFetches()
}

// openPairingFetches is the end of the time the fetches are held: the node has
// started, and the ones asked for meanwhile go out.
func (e *Engine) openPairingFetches() {
	e.pairing.fetchesOpen = true
	e.startPairingFetches()
}

// startPairingFetches hands the held fetches to the network, each carried by a
// worker of its own, when the network can be used.
func (e *Engine) startPairingFetches() {
	if e.net == nil || !e.pairing.fetchesOpen {
		return
	}
	held := e.pairing.held
	e.pairing.held = nil
	for _, f := range held {
		if !e.spawn(func(ctx context.Context) {
			c := &cmdPairingFetched{call: newCall[struct{}](), id: f.req.ID}
			c.answer, c.peer, c.err = e.net.PairingFetch(ctx, f.node, f.req.Addresses, f.req.Frame)
			_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
		}) {
			delete(e.pairing.fetches, f.req.ID) // the engine has ended
		}
	}
}

// cmdPairingFetched brings the answer of a result exchange, or why there is none,
// back to the loop.
type cmdPairingFetched struct {
	call[struct{}]
	id     pairing.ID
	answer []byte
	peer   PairingPeer
	err    error
}

func (c *cmdPairingFetched) gate() gate { return gateLoopback }
func (c *cmdPairingFetched) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.pairingFetched(now, c))
}

func (e *Engine) pairingFetched(now time.Time, c *cmdPairingFetched) error {
	if _, ok := e.pairing.fetches[c.id]; !ok {
		return nil
	}
	delete(e.pairing.fetches, c.id)
	if c.err != nil {
		e.log.Debug("a pairing fetch failed", "pairing", hex.EncodeToString(c.id[:]), "error", c.err.Error())
	}
	if err := e.canSign(); err != nil {
		// The answer is dropped, as a failed exchange is: the next tick that may sign
		// asks again.
		e.log.Debug("a pairing fetch answer is dropped: the node may not sign", "pairing", hex.EncodeToString(c.id[:]), "error", err.Error())
		return nil
	}
	var peer *peerCertificate
	if c.err == nil {
		peer = c.peer.certificate()
	}
	err := e.pairingCall(peer, func(w pairing.World) (pairing.State, []pairing.Action, error) {
		if c.err != nil {
			return e.pairing.state.OnFetchFailed(w, c.id, e.randomReader(), now)
		}
		return e.pairing.state.OnFetchResult(w, c.id, c.peer.Conn.Remote, c.answer, e.randomReader(), now)
	})
	e.pairingFollow(now)
	return err
}

// ---------------------------------------------------------------------------
// Sessions

// pairingCall runs one call of the state machine and carries out what it
// returned. peer is the accepted certificate a credential of this call is stored
// with.
func (e *Engine) pairingCall(peer *peerCertificate, call func(w pairing.World) (pairing.State, []pairing.Action, error)) error {
	next, acts, err := call(e.pairingWorld())
	if err != nil {
		return err
	}
	return e.pairingPerform(next, acts, peer)
}

// pairingEnded forgets a session whose stream is over and tells the state
// machine so.
func (e *Engine) pairingEnded(sid pairing.SessionID, s *pairingSession) error {
	delete(e.pairing.sessions, sid)
	if s.side == sideTarget {
		e.pairing.state = e.pairing.state.TargetClosed(sid)
		return nil
	}
	return e.pairingCall(nil, func(pairing.World) (pairing.State, []pairing.Action, error) {
		return e.pairing.state.InitiatorClosed(sid)
	})
}

type cmdPairingOpened struct {
	call[pairing.SessionID]
	peer   PairingPeer
	stream PairingStream
}

func (c *cmdPairingOpened) gate() gate { return gateLoopback }
func (c *cmdPairingOpened) run(e *Engine, now time.Time) {
	sid, err := e.pairingOpened(now, c.peer, c.stream)
	c.done(sid, err)
}

type cmdPairingFrame struct {
	call[struct{}]
	sid   pairing.SessionID
	frame []byte
}

func (c *cmdPairingFrame) gate() gate { return gateLoopback }
func (c *cmdPairingFrame) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.pairingFrame(now, c.sid, c.frame))
}

type cmdPairingClosed struct {
	call[struct{}]
	sid pairing.SessionID
}

func (c *cmdPairingClosed) gate() gate { return gateLoopback }
func (c *cmdPairingClosed) run(e *Engine, now time.Time) {
	c.done(struct{}{}, e.pairingClosed(now, c.sid))
}

// PairingOpened takes a pairing stream a peer opened, the target's end of an
// online segment or of a result fetch, and gives it the session identifier it is
// known by from then on. The network calls it from the goroutine that serves the
// stream; it runs in the loop and waits for it. An error refuses the stream.
func (e *Engine) PairingOpened(ctx context.Context, peer PairingPeer, stream PairingStream) (pairing.SessionID, error) {
	c := &cmdPairingOpened{call: newCall[pairing.SessionID](), peer: peer, stream: stream}
	return roundTrip(ctx, e, submitInternal, c, c.call)
}

// PairingFrame hands the engine one frame of the stream of a session. Whatever the
// frame makes the engine send or close has been issued when it returns: the
// network reads the next frame only after that. An error means the engine takes no
// more frames of the session, and has closed its stream.
func (e *Engine) PairingFrame(ctx context.Context, sid pairing.SessionID, frame []byte) error {
	c := &cmdPairingFrame{call: newCall[struct{}](), sid: sid, frame: frame}
	_, err := roundTrip(ctx, e, submitInternal, c, c.call)
	return err
}

// PairingClosed tells the engine that the stream of a session ended without the
// engine's own close. A session the engine does not hold is ignored.
func (e *Engine) PairingClosed(ctx context.Context, sid pairing.SessionID) {
	c := &cmdPairingClosed{call: newCall[struct{}](), sid: sid}
	_, _ = roundTrip(ctx, e, submitInternal, c, c.call)
}

var errPairingSession = errors.New("engine: no such pairing session")

// pairingOpened registers a target session and lets the state machine time it.
func (e *Engine) pairingOpened(now time.Time, peer PairingPeer, stream PairingStream) (pairing.SessionID, error) {
	if err := e.canSign(); err != nil {
		return 0, err
	}
	e.pairing.last++
	sid := e.pairing.last
	e.pairing.sessions[sid] = &pairingSession{side: sideTarget, stream: stream, peer: peer}
	err := e.pairingCall(nil, func(w pairing.World) (pairing.State, []pairing.Action, error) {
		return e.pairing.state.TargetOpen(w, sid, now)
	})
	if err != nil {
		delete(e.pairing.sessions, sid)
		e.pairingFollow(now)
		return 0, err
	}
	e.pairingFollow(now)
	return sid, nil
}

// pairingFrame hands a frame to the side the session is. An error of the call
// (not a protocol outcome, which is in the actions) ends the session.
func (e *Engine) pairingFrame(now time.Time, sid pairing.SessionID, frame []byte) error {
	s := e.pairing.sessions[sid]
	if s == nil {
		return errPairingSession
	}
	err := e.canSign()
	if err == nil {
		err = e.pairingCall(s.peer.certificate(), func(w pairing.World) (pairing.State, []pairing.Action, error) {
			if s.side == sideTarget {
				return e.pairing.state.TargetReceive(w, s.peer.Conn, sid, frame, e.randomReader(), now)
			}
			return e.pairing.state.InitiatorReceive(w, sid, frame, now)
		})
	}
	if err != nil {
		e.log.Warn("a pairing frame could not be handled; the stream is closed", "session", uint64(sid), "error", err.Error())
		s.stream.Close()
		if endErr := e.pairingEnded(sid, s); endErr != nil {
			e.log.Error("the end of a pairing session could not be taken in", "session", uint64(sid), "error", endErr.Error())
		}
	}
	e.pairingFollow(now)
	return err
}

// pairingClosed takes in a stream that ended without the engine's own close.
func (e *Engine) pairingClosed(now time.Time, sid pairing.SessionID) error {
	s := e.pairing.sessions[sid]
	if s == nil {
		// The network may report the end of a stream of a request before the loop
		// has registered it.
		if req := e.requestOf(sid); req != nil && !req.live {
			req.closedEarly = true
		}
		return nil
	}
	err := e.pairingEnded(sid, s)
	e.pairingFollow(now)
	return err
}

// ---------------------------------------------------------------------------
// Time

// pairingBusy says whether time alone may change something of a pairing: the
// recovery is due, a stream is open, a request waits for its owner, or an
// initiator's request has not ended. While the node may not sign nothing is, since
// the tick does not run.
// Records that have ended wait for the next tick that anything else causes to be
// removed; none is scheduled for them.
func (e *Engine) pairingBusy() bool {
	if e.d.Keys == nil || e.signRefusal() != 0 {
		return false // a tick of a node that may not sign does nothing; see pairingTick
	}
	if e.pairing.recoverDue || len(e.pairing.sessions) > 0 {
		return true
	}
	for _, r := range e.pairing.state.Initiators() {
		if !r.Status.Terminal() {
			return true
		}
	}
	for _, r := range e.pairing.state.Targets() {
		if r.Status == pairing.TargetPending {
			return true
		}
	}
	return false
}

var pairingTickKey = schedKey{schedPairingTick, ""}

// pairingSchedule points the schedule's entry of the pairing tick at one period
// from now while a pairing is under way, and removes it otherwise.
func (e *Engine) pairingSchedule(now time.Time) {
	if e.pairingBusy() {
		e.sched.set(pairingTickKey, now.Add(pairingTickEvery))
		return
	}
	e.sched.remove(pairingTickKey)
}

// pairingResume is called when the node may sign again after a pause of its clock:
// the tick that was not scheduled meanwhile is due at once.
func (e *Engine) pairingResume(now time.Time) {
	if e.pairingBusy() {
		e.sched.set(pairingTickKey, now)
	}
}

// pairingTick runs the state machine's Recover, when it is still due, and its
// Tick, and carries out their actions, then schedules the next. Both may sign (a
// revocation, the credential of a confirmed request) and Tick is the time rules
// that a clock in doubt cannot judge, so while the node may not sign neither is
// called and nothing is scheduled; pairingResume brings the tick back. A failure is
// logged and tried again after the retry backoff: it does not stop the loop unless
// the storage failed for good.
func (e *Engine) pairingTick(now time.Time) error {
	if e.d.Keys == nil {
		return nil
	}
	if e.signRefusal() != 0 {
		e.sched.remove(pairingTickKey)
		return nil
	}
	var err error
	if e.pairing.recoverDue {
		err = e.recoverPairing(now)
	}
	if err == nil {
		err = e.pairingCall(nil, func(w pairing.World) (pairing.State, []pairing.Action, error) {
			return e.pairing.state.Tick(w, e.randomReader(), now)
		})
	}
	if err != nil {
		e.log.Error("the pairing tick failed; it is tried again", "error", err.Error())
		if e.pairingBusy() {
			e.sched.set(pairingTickKey, now.Add(e.p.RetryBackoff))
		}
		return err
	}
	e.pairingSchedule(now)
	return nil
}

// pairingFollow is the tick that follows every command and event of the pairing,
// so that what a command made due, such as the exchange after a confirmation,
// happens at once. Its failure is logged by the tick.
func (e *Engine) pairingFollow(now time.Time) { _ = e.pairingTick(now) }
