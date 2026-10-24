package pairing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/credential"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/internal/revocation"
	"github.com/aianyai/sidera/internal/trust"
)

// This file is the test harness: a small simulated network of nodes that run
// the state machines the way a daemon would. It performs the actions in order,
// writes persisted records as bytes (so that every restart reads them back
// through the decoders), and keeps the credentials and revocations a node
// stores. It decides nothing itself.

const simProtocol = "/sidera/pairing/1.0.0"

// detReader is a deterministic io.Reader for randomness.
type detReader struct{ r *rand.Rand }

func (d detReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(d.r.Intn(256))
	}
	return len(p), nil
}

type simNode struct {
	t      testing.TB
	sim    *sim
	name   string
	signer record.Signer
	party  Party
	params Params
	rng    detReader

	state State
	disk  map[string][]byte

	credentials map[record.Digest]record.Record
	revocations map[record.Digest]record.Record // by credential digest
	view        trust.View
	retired     *revocation.Set
	list        admin.List
	persons     map[Author]admin.Actor
	stored      map[admin.RequestKey]admin.StoredRequest
	notices     []Notice
	opCounter   uint64

	nextSID SessionID

	// crashAt, when positive, makes the node crash before it performs the
	// crashAt-th action counted from the start of the run (applied counts them).
	crashAt, applied int
	// crashed is set when a crash happened inside apply; the caller that was
	// about to send a frame after the actions then does not send it.
	crashed bool
}

type simStream struct {
	id                int
	initiator, target *simNode
	initSID, tgtSID   SessionID
	// initClosed and tgtClosed are set when that end closed the stream; frames
	// still queued toward an end that closed are dropped.
	initClosed, tgtClosed bool
}

type simDelivery struct {
	stream   *simStream
	toTarget bool
	frame    []byte
	// closing marks the end of the stream instead of a frame.
	closing bool
}

type simFetch struct {
	from  *simNode
	id    ID
	frame []byte
	to    ident.NodeID
}

type sim struct {
	t       testing.TB
	now     time.Time
	nodes   []*simNode
	streams []*simStream
	queue   []simDelivery
	fetches []simFetch
	events  []simEvent
	// lossy makes the delivery of fetch exchanges fail on demand.
	dropFetch func(simFetch) bool
}

// simEvent is one entry of the log that the property test's model reads.
type simEvent struct {
	At   time.Time
	Node string
	Kind string
	ID   ID
	Peer ident.NodeID
	Aux  string
	// Frame is set on sent frames and, for record events, holds the persisted
	// bytes.
	Frame []byte
	// Times and Nodes carry the persisted counters and expectations.
	Times []time.Time
	Nodes []ident.NodeID
}

func newSim(t testing.TB) *sim { return &sim{t: t, now: testNow} }

func (s *sim) log(e simEvent) { e.At = s.now; s.events = append(s.events, e) }

func (s *sim) addNode(name, identSeed, netSeed string, serial uint64, seed int64) *simNode {
	t := s.t
	p := partyFrom(t, identSeed, netSeed, serial)
	n := &simNode{
		t: t, sim: s, name: name, signer: signerFromSeed(t, identSeed), party: p, params: DefaultParams(),
		rng:  detReader{rand.New(rand.NewSource(seed))},
		disk: map[string][]byte{}, credentials: map[record.Digest]record.Record{}, revocations: map[record.Digest]record.Record{},
		retired: revocation.NewSet(), persons: map[Author]admin.Actor{}, stored: map[admin.RequestKey]admin.StoredRequest{},
		nextSID: 1,
	}
	n.view = trust.View{Local: p.node, Settings: trust.Settings{GracePeriod: trust.DefaultGracePeriod}, RevokedCredentials: trust.DigestSet{}, LocallyRevoked: trust.NodeSet{}}
	n.list, _ = admin.NewList(nil)
	n.state, _ = Restore(Snapshot{})
	s.nodes = append(s.nodes, n)
	return n
}

func (s *sim) nodeByID(id ident.NodeID) *simNode {
	for _, n := range s.nodes {
		if n.party.node == id {
			return n
		}
	}
	return nil
}

// standardPair returns the two nodes of the vectors (A: TEST 1 / TEST 2, B:
// TEST 3 / TEST SHA).
func standardPair(t testing.TB) (*sim, *simNode, *simNode) {
	s := newSim(t)
	a := s.addNode("A", seedTest1, seedTest2, 1, 1)
	b := s.addNode("B", seedTest3, seedTestSHA, 7, 2)
	return s, a, b
}

func (n *simNode) world() World {
	return World{
		Params: n.params, Identity: n.signer, Revocations: n.retired, Trust: n.view, List: n.list,
		Actors: func(a Author) admin.Actor { return n.persons[a] },
		Stored: func(k admin.RequestKey) *admin.StoredRequest {
			if r, ok := n.stored[k]; ok {
				return &r
			}
			return nil
		},
	}
}

func (n *simNode) conn(to *simNode) Conn {
	return Conn{Local: n.party, Remote: to.party, Protocol: simProtocol}
}

// apply performs actions in order, as a daemon would.
func (n *simNode) apply(actions []Action, stream *simStream) {
	t := n.t
	for _, a := range actions {
		if n.crashAt > 0 && n.applied == n.crashAt {
			// The node crashes before this action: the ones before it happened,
			// the rest, and the state they came from, are lost.
			n.crashAt = 0
			n.crashed = true
			n.restart()
			return
		}
		n.applied++
		switch a := a.(type) {
		case Send:
			st, toTarget := n.streamOf(a.Session)
			if st == nil {
				continue
			}
			n.sim.log(simEvent{Node: n.name, Kind: "send", Frame: a.Frame, Peer: n.peerOf(st).party.node})
			n.sim.queue = append(n.sim.queue, simDelivery{stream: st, toTarget: toTarget, frame: a.Frame})
		case Close:
			if st, toTarget := n.streamOf(a.Session); st != nil {
				if toTarget { // n is the initiator
					st.initClosed = true
				} else {
					st.tgtClosed = true
				}
				n.sim.queue = append(n.sim.queue, simDelivery{stream: st, toTarget: toTarget, closing: true})
			}
		case FetchRequest:
			rec, _ := n.state.Initiator(a.ID)
			to := rec.Transcript.terms.Target.node
			n.sim.log(simEvent{Node: n.name, Kind: "fetch", ID: a.ID, Peer: to, Frame: a.Frame})
			n.sim.fetches = append(n.sim.fetches, simFetch{from: n, id: a.ID, frame: a.Frame, to: to})
		case PersistLimits:
			b, err := a.Limits.Encode()
			must(t, err)
			l, err := DecodeLimits(b)
			must(t, err)
			b2, _ := l.Encode()
			if !bytes.Equal(b, b2) {
				t.Fatal("limits do not round trip")
			}
			n.disk["limits"] = b
			n.sim.log(simEvent{Node: n.name, Kind: "limits", Times: a.Limits.Reveals()})
		case PersistExpectations:
			b, err := EncodeExpectations(a.Expectations)
			must(t, err)
			if _, err := DecodeExpectations(b); err != nil {
				t.Fatal(err)
			}
			n.disk["expectations"] = b
			var nodes []ident.NodeID
			for _, e := range a.Expectations {
				nodes = append(nodes, e.Node)
			}
			n.sim.log(simEvent{Node: n.name, Kind: "expectations", Nodes: nodes})
		case PersistTarget:
			b, err := a.Record.Encode()
			must(t, err)
			r, err := DecodeTargetRecord(b)
			must(t, err)
			if r.ID() != a.Record.ID() {
				t.Fatal("target record id changed in a round trip")
			}
			if b2, _ := r.Encode(); !bytes.Equal(b, b2) {
				t.Fatal("target record does not round trip")
			}
			n.disk["target/"+fmt.Sprintf("%x", a.Record.ID())] = b
			n.sim.log(simEvent{Node: n.name, Kind: "record", Aux: "target", ID: a.Record.ID(), Frame: b})
		case DeleteTarget:
			delete(n.disk, "target/"+fmt.Sprintf("%x", a.ID))
		case PersistInitiator:
			b, err := a.Record.Encode()
			must(t, err)
			r, err := DecodeInitiatorRecord(b)
			must(t, err)
			if b2, _ := r.Encode(); !bytes.Equal(b, b2) {
				t.Fatal("initiator record does not round trip")
			}
			n.disk["initiator/"+fmt.Sprintf("%x", a.Record.ID())] = b
			n.sim.log(simEvent{Node: n.name, Kind: "record", Aux: "initiator", ID: a.Record.ID(), Frame: b})
		case DeleteInitiator:
			delete(n.disk, "initiator/"+fmt.Sprintf("%x", a.ID))
		case StoreCredential:
			d := a.Credential.Digest()
			n.credentials[d] = a.Record
			facts := credential.Facts(a.Credential)
			if !hasCred(n.view.Credentials, facts.Digest) {
				n.view.Credentials = append(append([]trust.Credential(nil), n.view.Credentials...), facts)
			}
			n.sim.log(simEvent{Node: n.name, Kind: "credential", Aux: d.String()})
		case StoreRevocation:
			ver, err := record.Verify(credential.RevocationDescriptor, a.Record)
			must(t, err)
			rev := ver.Payload()
			n.revocations[record.Digest(rev.Digest())] = a.Record
			n.view.RevokedCredentials[rev.Digest()] = struct{}{}
			n.sim.log(simEvent{Node: n.name, Kind: "revocation", Aux: record.Digest(rev.Digest()).String()})
		case Notify:
			n.notices = append(n.notices, a.Notice)
			n.sim.log(simEvent{Node: n.name, Kind: "notice", Aux: fmt.Sprint(a.Notice.Kind), ID: a.Notice.ID})
		default:
			t.Fatalf("unknown action %T", a)
		}
	}
}

func hasCred(cs []trust.Credential, d trust.CredentialDigest) bool {
	for _, c := range cs {
		if c.Digest == d {
			return true
		}
	}
	return false
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// streamOf finds the stream a node's session id belongs to and whether the node
// is the target of it.
func (n *simNode) streamOf(sid SessionID) (*simStream, bool) {
	for _, st := range n.sim.streams {
		if st.initiator == n && st.initSID == sid {
			return st, true // frames from the initiator go to the target
		}
		if st.target == n && st.tgtSID == sid {
			return st, false
		}
	}
	return nil, false
}

func (n *simNode) peerOf(st *simStream) *simNode {
	if st.initiator == n {
		return st.target
	}
	return st.initiator
}

// operation returns a fresh management request from the local owner.
func (n *simNode) owner() Request {
	n.opCounter++
	var id admin.OperationID
	binary.BigEndian.PutUint64(id[8:], n.opCounter)
	id[0] = n.name[0]
	return Request{Actor: admin.LocalOwner(), ID: id}
}

func (n *simNode) remember(out Outcome, req Request) {
	if out.Disposition == DispositionExecuted {
		n.stored[out.Key] = admin.StoredRequest{Fingerprint: out.Fingerprint}
	}
}

// initiate starts an online segment from n to to. It returns the stream.
func (s *sim) initiate(from, to *simNode, in Initiation) (*simStream, Outcome) {
	st := &simStream{id: len(s.streams), initiator: from, target: to, initSID: from.nextSID, tgtSID: to.nextSID}
	from.nextSID++
	to.nextSID++
	s.streams = append(s.streams, st)
	next, _, err := to.state.TargetOpen(to.world(), st.tgtSID, s.now)
	must(s.t, err)
	to.state = next
	req := from.owner()
	next, out, actions, err := from.state.Initiate(from.world(), from.conn(to), st.initSID, req, in, from.rng, s.now)
	must(s.t, err)
	from.state = next
	from.remember(out, req)
	if out.Disposition == DispositionExecuted && out.Result == ResultOK {
		from.apply(actions, st)
	} else {
		st.initClosed = true
	}
	return st, out
}

// deliverOne delivers the first queued frame (or drops it when drop is true).
func (s *sim) deliverOne(i int, drop bool) {
	d := s.queue[i]
	s.queue = append(s.queue[:i], s.queue[i+1:]...)
	st := d.stream
	if drop {
		return
	}
	if d.closing {
		if d.toTarget && !st.tgtClosed {
			st.tgtClosed = true
			st.target.state = st.target.state.TargetClosed(st.tgtSID)
		} else if !d.toTarget && !st.initClosed {
			st.initClosed = true
			n := st.initiator
			next, actions, err := n.state.InitiatorClosed(st.initSID)
			must(s.t, err)
			n.state = next
			n.apply(actions, st)
		}
		return
	}
	if drop || (d.toTarget && st.tgtClosed) || (!d.toTarget && st.initClosed) {
		return
	}
	if d.toTarget {
		n := st.target
		next, actions, err := n.state.TargetReceive(n.world(), n.conn(st.initiator), st.tgtSID, d.frame, n.rng, s.now)
		if err != nil {
			s.t.Fatalf("%s TargetReceive: %v", n.name, err)
		}
		n.state = next
		n.apply(actions, st)
		return
	}
	n := st.initiator
	next, actions, err := n.state.InitiatorReceive(n.world(), st.initSID, d.frame, s.now)
	must(s.t, err)
	n.state = next
	n.apply(actions, st)
}

// runFetch performs one result exchange: the target answers on a fresh stream.
func (s *sim) runFetch(i int, drop bool) {
	f := s.fetches[i]
	s.fetches = append(s.fetches[:i], s.fetches[i+1:]...)
	from := f.from
	to := s.nodeByID(f.to)
	fail := func() {
		next, actions, err := from.state.OnFetchFailed(from.world(), f.id, from.rng, s.now)
		must(s.t, err)
		from.state = next
		from.apply(actions, nil)
	}
	if drop || to == nil || (s.dropFetch != nil && s.dropFetch(f)) {
		fail()
		return
	}
	tsid := to.nextSID
	to.nextSID++
	st := &simStream{id: len(s.streams), initiator: from, target: to, initSID: 0, tgtSID: tsid}
	s.streams = append(s.streams, st)
	next, _, err := to.state.TargetOpen(to.world(), tsid, s.now)
	must(s.t, err)
	to.state = next
	next, actions, err := to.state.TargetReceive(to.world(), to.conn(from), tsid, f.frame, to.rng, s.now)
	must(s.t, err)
	to.state = next
	// The answer is the last action; every action before it is performed first,
	// and a crash on the way means the answer is never sent.
	var reply []byte
	to.crashed = false
	for _, a := range actions {
		if snd, ok := a.(Send); ok {
			reply = snd.Frame
			continue
		}
		to.apply([]Action{a}, st)
		if to.crashed {
			break
		}
	}
	if reply == nil || to.crashed {
		to.crashed = false
		fail()
		return
	}
	next, actions, err = from.state.OnFetchResult(from.world(), f.id, to.party, reply, from.rng, s.now)
	must(s.t, err)
	from.state = next
	from.apply(actions, nil)
}

// pump delivers everything queued, in order, and runs the fetch exchanges, until
// nothing is pending.
func (s *sim) pump() {
	for rounds := 0; rounds < 1000; rounds++ {
		if len(s.queue) == 0 && len(s.fetches) == 0 {
			return
		}
		for len(s.queue) > 0 {
			s.deliverOne(0, false)
		}
		for len(s.fetches) > 0 {
			s.runFetch(0, false)
		}
	}
	s.t.Fatal("pump did not settle")
}

// tick runs the periodic work of every node and settles the network.
func (s *sim) tick() {
	for _, n := range s.nodes {
		n.tick()
	}
}

func (n *simNode) tick() {
	next, actions, err := n.state.Tick(n.world(), n.rng, n.sim.now)
	must(n.t, err)
	n.state = next
	n.apply(actions, nil)
}

func (s *sim) settle() {
	for i := 0; i < 20; i++ {
		s.tick()
		s.pump()
	}
}

func (s *sim) advance(d time.Duration) { s.now = s.now.Add(d) }

// run lets time pass in steps, ticking and delivering at each, so schedules
// that wait (the backoff of the result exchanges) come due.
func (s *sim) run(total, step time.Duration) {
	for elapsed := time.Duration(0); elapsed < total; elapsed += step {
		s.advance(step)
		s.settle()
	}
}

// restart simulates a crash: volatile state is lost, persisted records are read
// back through the decoders, and Recover runs.
func (n *simNode) restart() {
	snap := Snapshot{}
	var err error
	if b, ok := n.disk["limits"]; ok {
		snap.Limits, err = DecodeLimits(b)
		must(n.t, err)
	}
	if b, ok := n.disk["expectations"]; ok {
		snap.Expectations, err = DecodeExpectations(b)
		must(n.t, err)
	}
	for k, b := range n.disk {
		switch {
		case len(k) > 7 && k[:7] == "target/":
			r, err := DecodeTargetRecord(b)
			must(n.t, err)
			if k != "target/"+fmt.Sprintf("%x", r.ID()) {
				n.t.Fatalf("record stored under %s has id %x", k, r.ID())
			}
			snap.Targets = append(snap.Targets, r)
		case len(k) > 10 && k[:10] == "initiator/":
			r, err := DecodeInitiatorRecord(b)
			must(n.t, err)
			snap.Initiators = append(snap.Initiators, r)
		}
	}
	sortSnapshot(&snap)
	n.state, err = Restore(snap)
	must(n.t, err)
	next, actions, err := n.state.Recover(n.world(), n.sim.now)
	must(n.t, err)
	n.state = next
	n.apply(actions, nil)
	// streams of this node are gone
	for _, st := range n.sim.streams {
		if st.initiator == n && !st.initClosed {
			st.initClosed = true
			n.sim.queue = append(n.sim.queue, simDelivery{stream: st, toTarget: true, closing: true})
		}
		if st.target == n && !st.tgtClosed {
			st.tgtClosed = true
			n.sim.queue = append(n.sim.queue, simDelivery{stream: st, toTarget: false, closing: true})
		}
	}
}

func sortSnapshot(s *Snapshot) {
	less := func(a, b ID) bool { return bytes.Compare(a[:], b[:]) < 0 }
	for i := range s.Targets {
		for j := i + 1; j < len(s.Targets); j++ {
			if less(s.Targets[j].ID(), s.Targets[i].ID()) {
				s.Targets[i], s.Targets[j] = s.Targets[j], s.Targets[i]
			}
		}
	}
	for i := range s.Initiators {
		for j := i + 1; j < len(s.Initiators); j++ {
			if less(s.Initiators[j].ID(), s.Initiators[i].ID()) {
				s.Initiators[i], s.Initiators[j] = s.Initiators[j], s.Initiators[i]
			}
		}
	}
}

// pendingFrom returns the target record of the request that came from a node.
func (n *simNode) pendingFrom(from *simNode) (TargetRecord, bool) {
	for _, r := range n.state.Targets() {
		if r.Transcript.terms.Initiator.node == from.party.node && r.Status == TargetPending {
			return r, true
		}
	}
	return TargetRecord{}, false
}

func (n *simNode) initiatedTo(to *simNode) (InitiatorRecord, bool) {
	var last InitiatorRecord
	found := false
	for _, r := range n.state.Initiators() {
		if r.Transcript.terms.Target.node == to.party.node {
			last, found = r, true
		}
	}
	return last, found
}

// confirm has the local owner of n confirm the request id with code.
func (n *simNode) confirm(id ID, code Code) Outcome {
	req := n.owner()
	next, out, actions, err := n.state.Confirm(n.world(), req, id, code, n.sim.now)
	must(n.t, err)
	n.state = next
	n.remember(out, req)
	if out.Disposition == DispositionExecuted && out.Result == ResultOK {
		n.sim.log(simEvent{Node: n.name, Kind: "confirm", ID: id, Aux: "code"})
	}
	n.apply(actions, nil)
	return out
}

func (n *simNode) reject(id ID, reason WithdrawReason) Outcome {
	req := n.owner()
	next, out, actions, err := n.state.Reject(n.world(), req, id, reason, n.sim.now)
	must(n.t, err)
	n.state = next
	n.remember(out, req)
	if out.Disposition == DispositionExecuted && out.Result == ResultOK {
		n.sim.log(simEvent{Node: n.name, Kind: "reject", ID: id, Aux: reason.String()})
	}
	n.apply(actions, nil)
	return out
}

func (n *simNode) expect(other *simNode) Outcome {
	req := n.owner()
	next, out, actions, err := n.state.Expect(n.world(), req, other.party.node, n.sim.now)
	must(n.t, err)
	n.state = next
	n.remember(out, req)
	n.apply(actions, nil)
	return out
}

// code returns the code the node shows for a request.
func (n *simNode) codeOf(id ID) Code {
	if r, ok := n.state.Target(id); ok {
		return r.Transcript.Code()
	}
	if r, ok := n.state.Initiator(id); ok {
		return r.Transcript.Code()
	}
	n.t.Fatalf("%s has no request %x", n.name, id)
	return 0
}

// oracle: an independent implementation of the identifier and the code from the
// transcript, written from the construction and sharing nothing with it.
func oracle(t testing.TB, tr Transcript) (ID, Code) {
	t.Helper()
	terms := tr.Terms()
	fields := framedFields(oracleFields(terms))
	c := tr.Commitment()
	nB, nA := tr.Nonces()
	body := append(append(append(fields, uvarintBytes(32)...), c[:]...), uvarintBytes(32)...)
	body = append(append(append(body, nB[:]...), uvarintBytes(32)...), nA[:]...)
	lp := func(b []byte) []byte { return append(uvarintBytes(len(b)), b...) }
	id := sha256.Sum256(append(lp([]byte("sidera/pairing/transcript/1")), body...))
	// HKDF by hand: extract with a zero salt, expand one block.
	ikm := append(append([]byte(nil), nA[:]...), nB[:]...)
	prk := hmacSHA256(make([]byte, 32), ikm)
	info := append(lp([]byte("sidera/pairing/sas/1")), body...)
	okm := hmacSHA256(prk, append(info, 1))
	// a second block for the 32 bytes
	okm2 := hmacSHA256(prk, append(append(append([]byte(nil), okm...), info...), 2))
	all := append(okm, okm2...)[:32]
	for i := 0; i < 8; i++ {
		v := binary.BigEndian.Uint32(all[4*i:])
		if v < 4294000000 {
			return ID(id), Code(v % 1000000)
		}
	}
	t.Fatal("oracle found no code")
	return ID(id), 0
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// oracleFields lists the fields of the terms in the order of the construction,
// written out here from the accessors: the protocol, then for each party the
// node, the two keys with their algorithm tag in the fixed 38-byte encoding and
// the serial, then the two times.
func oracleFields(t Terms) [][]byte {
	key := func(k ident.PublicKey) []byte {
		return append([]byte{0xa2, 0x01, 0x01, 0x02, 0x58, 0x20}, k.Bytes()...)
	}
	be := func(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }
	out := [][]byte{[]byte(t.Protocol)}
	for _, p := range []Party{t.Initiator, t.Target} {
		out = append(out, p.Node().Bytes(), key(p.Identity()), key(p.Network()), be(p.Serial()))
	}
	out = append(out, be(uint64(t.CreatedAt.Unix())))
	if t.NotAfter.IsZero() {
		out = append(out, nil)
	} else {
		out = append(out, be(uint64(t.NotAfter.Unix())))
	}
	return out
}

// seedHex derives a deterministic Ed25519 seed for generated test nodes.
func seedHex(label string, i int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("pairing-test/%s/%d", label, i)))
	return fmt.Sprintf("%x", sum[:])
}

// addGenerated adds node number i with generated keys.
func (s *sim) addGenerated(name string, i int) *simNode {
	return s.addNode(name, seedHex("identity", i), seedHex("network", i), uint64(1+i%5), int64(100+i))
}

// grantPerson puts a remote person on the node's authorized list at a level and
// returns the author and the actor (with a certificate that grants everything
// the list does).
func (n *simNode) grantPerson(i int, level admin.Level) (Author, admin.Actor) {
	key := signerFromSeed(n.t, seedHex("person", i)).Public()
	pid, err := ident.PersonIDFromKey(key)
	must(n.t, err)
	entries := n.list.Entries()
	entries = append(entries, admin.Entry{Person: pid, Level: level})
	list, problems := admin.NewList(entries)
	if len(problems) != 0 {
		n.t.Fatalf("list problems: %v", problems)
	}
	n.list = list
	actor := admin.RemotePerson(pid, admin.DeviceCert{
		MaxLevel: admin.LevelFullControl, NotBefore: testNow.Add(-24 * time.Hour), NotAfter: testNow.Add(10 * 24 * time.Hour),
		Revocation: admin.RevocationClear,
	})
	author, err := PersonAuthor(pid)
	must(n.t, err)
	n.persons[author] = actor
	return author, actor
}

// as runs a request as a given actor.
func (n *simNode) as(actor admin.Actor) Request {
	r := n.owner()
	r.Actor = actor
	return r
}

// seedPersisted adds, as fuzz seeds, the persisted records of a finished
// pairing at several stages.
func seedPersisted(f *testing.F, pick func(s *sim, a, b *simNode) [][]byte) {
	t := &testing.T{}
	_ = t
	for _, stage := range []int{0, 1, 2} {
		s, a, b := standardPair(f)
		ia, tb := startPairing(f, s, a, b)
		if stage >= 1 {
			a.confirm(ia.ID(), ia.Transcript.Code())
			s.run(10*time.Second, time.Second)
		}
		if stage >= 2 {
			b.confirm(tb.ID(), tb.Transcript.Code())
			s.run(2*time.Minute, 10*time.Second)
		}
		for _, seed := range pick(s, a, b) {
			f.Add(seed)
		}
	}
}

// oracleCredentialDigest is the digest of the credential the transcript's two
// sides must both sign, computed from the construction with its own HKDF.
func oracleCredentialDigest(t testing.TB, tr Transcript) record.Digest {
	t.Helper()
	terms := tr.Terms()
	fields := framedFields(oracleFields(terms))
	c := tr.Commitment()
	nB, nA := tr.Nonces()
	lp := func(b []byte) []byte { return append(uvarintBytes(len(b)), b...) }
	body := append(append(append(fields, uvarintBytes(32)...), c[:]...), uvarintBytes(32)...)
	body = append(append(append(body, nB[:]...), uvarintBytes(32)...), nA[:]...)
	ikm := append(append([]byte(nil), nA[:]...), nB[:]...)
	prk := hmacSHA256(make([]byte, 32), ikm)
	info := append(lp([]byte("sidera/pairing/credential-nonce/1")), body...)
	okm := hmacSHA256(prk, append(info, 1))[:16]
	var nonce credential.Nonce
	copy(nonce[:], okm)
	cred := credential.New(terms.Initiator.Node(), terms.Target.Node(), terms.CreatedAt, terms.NotAfter, nonce)
	d, err := cred.Digest()
	must(t, err)
	return d
}
