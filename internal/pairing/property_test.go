package pairing

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
)

// The property test runs the state machines of three nodes together under
// random schedules: two honest nodes A and B, and an attacker M that is a node
// like the others (it runs the same state machines, with keys of its own) and
// starts online segments to either at will, aborts them at the worst moment and
// starts again. Frames are lost, duplicated and reordered, result exchanges
// fail, nodes crash between any two actions and restart from what they had
// persisted, owners confirm with the right code, a wrong code, a report of
// differing codes or a refusal, and expectations come and go.
//
// A model reads the log of the run and checks, independently of the state
// machines (it recomputes identifiers, codes and credential digests with its
// own implementation and counts reveals from the persisted counters):
//
//   - a node stores a credential only after its own owner confirmed (or an
//     expectation did) and the other end confirmed too, and the credential is
//     the one the transcript determines;
//   - for one pairing there is one credential;
//   - no signature of the initiator leaves before its confirmation and before
//     it was persisted;
//   - every reveal sent to a node that was not expected was counted and
//     persisted before it was sent, and no sliding hour holds more than the
//     budget;
//   - a signature that left a node and did not end in the complete credential
//     is revoked, at once when the owner withdraws it.

type propModel struct {
	t *testing.T
	s *sim
	// budget is the reveal budget per sliding hour that the run configured.
	budget int

	next      int
	confirmed map[string]int // node + id -> event index of the confirmation
	released  map[string]bool
	spent     map[string][]time.Time // persisted reveal times already seen, by node
	unmatched map[string]int         // persisted reveals not yet matched by a sent M2
	expecting map[string]map[ident.NodeID]bool
	lastLimit map[string][]time.Time

	transcripts map[ID]Transcript
	credByPair  map[ID]record.Digest
	stored      map[string]int // node + id -> credentials stored
	revoked     map[string]map[record.Digest]bool
	signedSent  map[string]ID // node + id -> id of a pairing whose signature was sent
	m2sentToB   int
}

func newPropModel(t *testing.T, s *sim) *propModel {
	return &propModel{
		t: t, s: s, budget: 5,
		confirmed: map[string]int{}, released: map[string]bool{},
		spent: map[string][]time.Time{}, unmatched: map[string]int{},
		expecting: map[string]map[ident.NodeID]bool{}, lastLimit: map[string][]time.Time{},
		transcripts: map[ID]Transcript{}, credByPair: map[ID]record.Digest{},
		stored: map[string]int{}, revoked: map[string]map[record.Digest]bool{}, signedSent: map[string]ID{},
	}
}

func key(node string, id ID) string { return fmt.Sprintf("%s/%x", node, id) }

// observe records a transcript and checks the machine's identifier and code
// against the oracle.
func (m *propModel) observe(tr Transcript) {
	id, code := oracle(m.t, tr)
	if id != tr.ID() || code != tr.Code() {
		m.t.Fatalf("identifier or code differs from the independent derivation: %x/%v vs %x/%v", tr.ID(), tr.Code(), id, code)
	}
	m.transcripts[id] = tr
}

func (m *propModel) pairFor(id ID) (Transcript, bool) { tr, ok := m.transcripts[id]; return tr, ok }

// check processes the events since the last call.
func (m *propModel) check() {
	for ; m.next < len(m.s.events); m.next++ {
		e := m.s.events[m.next]
		switch e.Kind {
		case "record":
			m.onRecord(e)
		case "confirm":
			m.confirmed[key(e.Node, e.ID)] = m.next
		case "limits":
			m.onLimits(e)
		case "expectations":
			set := map[ident.NodeID]bool{}
			for _, n := range e.Nodes {
				set[n] = true
			}
			m.expecting[e.Node] = set
		case "send":
			m.onSend(e)
		case "fetch":
			m.onFetch(e)
		case "credential":
			m.onCredential(e)
		case "revocation":
			if m.revoked[e.Node] == nil {
				m.revoked[e.Node] = map[record.Digest]bool{}
			}
			m.revoked[e.Node][mustDigest(m.t, e.Aux)] = true
		}
	}
}

func mustDigest(t testing.TB, hex string) record.Digest {
	var d record.Digest
	b := mustHex(t, hex)
	copy(d[:], b)
	return d
}

func (m *propModel) onRecord(e simEvent) {
	var tr Transcript
	var confirmedByExpectation bool
	if e.Aux == "target" {
		r, err := DecodeTargetRecord(e.Frame)
		must(m.t, err)
		tr = r.Transcript
		confirmedByExpectation = r.Confirmation != nil && r.Confirmation.Method == ConfirmedByExpectation
	} else {
		r, err := DecodeInitiatorRecord(e.Frame)
		must(m.t, err)
		tr = r.Transcript
		confirmedByExpectation = r.Confirmation != nil && r.Confirmation.Method == ConfirmedByExpectation
		if r.Released {
			m.released[key(e.Node, tr.ID())] = true
		}
	}
	m.observe(tr)
	if confirmedByExpectation {
		if _, ok := m.confirmed[key(e.Node, tr.ID())]; !ok {
			m.confirmed[key(e.Node, tr.ID())] = m.next
		}
	}
}

func (m *propModel) onLimits(e simEvent) {
	// New reveals are the entries of the persisted list that were not in the
	// previous one.
	prev := map[time.Time]int{}
	for _, t := range m.lastLimit[e.Node] {
		prev[t]++
	}
	for _, t := range e.Times {
		if prev[t] > 0 {
			prev[t]--
			continue
		}
		m.spent[e.Node] = append(m.spent[e.Node], t)
		m.unmatched[e.Node]++
		// No sliding hour holds more than the budget.
		n := 0
		for _, x := range m.spent[e.Node] {
			if t.Sub(x) < time.Hour && !x.After(t) {
				n++
			}
		}
		if n > m.budget {
			m.t.Fatalf("%s spent %d reveals within an hour, budget %d", e.Node, n, m.budget)
		}
	}
	m.lastLimit[e.Node] = e.Times
}

func (m *propModel) onSend(e simEvent) {
	r, err := DecodeReveal(e.Frame)
	if err != nil || r.NB.IsZero() {
		return
	}
	// A reveal is sent to an expected node, or was counted and persisted first.
	if m.expecting[e.Node][e.Peer] {
		return
	}
	if m.unmatched[e.Node] == 0 {
		m.t.Fatalf("%s revealed its nonce to %v without a persisted count", e.Node, e.Peer)
	}
	m.unmatched[e.Node]--
	if e.Node == "B" {
		if peer := m.s.nodeByID(e.Peer); peer != nil && peer.name == "M" {
			m.m2sentToB++
		}
	}
}

func (m *propModel) onFetch(e simEvent) {
	f, err := DecodeFetch(e.Frame)
	must(m.t, err)
	if f.Signed == nil {
		return
	}
	if _, ok := m.confirmed[key(e.Node, f.ID)]; !ok {
		m.t.Fatalf("%s sent a signature for %x before it confirmed", e.Node, f.ID)
	}
	if !m.released[key(e.Node, f.ID)] {
		m.t.Fatalf("%s sent a signature for %x before persisting that it was released", e.Node, f.ID)
	}
	m.signedSent[key(e.Node, f.ID)] = f.ID
}

func (m *propModel) onCredential(e simEvent) {
	d := mustDigest(m.t, e.Aux)
	// Find the pairing the digest belongs to.
	var id ID
	found := false
	for pid, tr := range m.transcripts {
		if oracleCredentialDigest(m.t, tr) == d {
			id, found = pid, true
			break
		}
	}
	if !found {
		m.t.Fatalf("%s stored a credential that no observed transcript determines", e.Node)
	}
	tr := m.transcripts[id]
	k := key(e.Node, id)
	// Storing the same credential again after a restart is allowed; storing a
	// different one for the pairing is not.
	m.stored[k]++
	if prev, ok := m.credByPair[id]; ok && prev != d {
		m.t.Fatalf("two different credentials for pairing %x", id)
	}
	m.credByPair[id] = d
	// Both ends confirmed before this credential existed.
	for _, p := range []Party{tr.Terms().Initiator, tr.Terms().Target} {
		n := m.s.nodeByID(p.Node())
		if n == nil {
			continue
		}
		if _, ok := m.confirmed[key(n.name, id)]; !ok {
			m.t.Fatalf("%s stored the credential of %x but %s had not confirmed", e.Node, id, n.name)
		}
	}
}

// finalChecks run after the network has been quiet for long enough for every
// request to end.
func (m *propModel) finalChecks(nodes ...*simNode) {
	m.check()
	for _, n := range nodes {
		for _, r := range n.state.Initiators() {
			if !r.Status.Terminal() {
				m.t.Fatalf("%s: initiator record %x is still %v", n.name, r.ID(), r.Status)
			}
			k := key(n.name, r.ID())
			d := oracleCredentialDigest(m.t, r.Transcript)
			sent := m.signedSent[k] == r.ID() && m.released[k]
			switch {
			case r.Status == InitiatorComplete:
				if m.revoked[n.name][d] {
					m.t.Fatalf("%s revoked a credential that completed", n.name)
				}
			case sent || r.Released:
				if !m.revoked[n.name][d] {
					m.t.Fatalf("%s: a signature left (%v) but credential of %x is not revoked", n.name, r.Status, r.ID())
				}
			default:
				if m.revoked[n.name][d] {
					m.t.Fatalf("%s revoked a credential whose signature never left", n.name)
				}
			}
		}
		for _, r := range n.state.Targets() {
			if !r.Terminal() {
				m.t.Fatalf("%s: target record %x is still %v", n.name, r.ID(), r.Status)
			}
		}
		if len(n.state.sessions) != 0 {
			m.t.Fatalf("%s: %d online sessions linger", n.name, len(n.state.sessions))
		}
	}
	// One side holding a credential means the other either holds it or revoked it.
	for id, tr := range m.transcripts {
		d := oracleCredentialDigest(m.t, tr)
		init := m.s.nodeByID(tr.Terms().Initiator.Node())
		tgt := m.s.nodeByID(tr.Terms().Target.Node())
		if init == nil || tgt == nil {
			continue
		}
		_, ia := init.credentials[d]
		_, ta := tgt.credentials[d]
		if ia && !ta {
			m.t.Fatalf("pairing %x: the initiator stored a credential the target never signed", id)
		}
		if ta && !ia && !m.revoked[init.name][d] {
			m.t.Fatalf("pairing %x: the target holds a credential the initiator neither has nor revoked", id)
		}
	}
}

type propRun struct {
	// hostile makes owners report differing codes often, so the failure pause
	// is reached.
	hostile bool
	t       *testing.T
	rng     *rand.Rand
	s       *sim
	a       *simNode
	b       *simNode
	m       *simNode
	// strangers are more nodes that start segments to the others.
	strangers []*simNode
	mod       *propModel
	cov       map[string]int
}

func newPropRun(t *testing.T, seed int64) *propRun {
	s := newSim(t)
	r := &propRun{hostile: seed%3 == 0, t: t, rng: rand.New(rand.NewSource(seed)), s: s, cov: map[string]int{}}
	r.a = s.addNode("A", seedTest1, seedTest2, 1, seed*3+1)
	r.b = s.addNode("B", seedTest3, seedTestSHA, 7, seed*3+2)
	r.m = s.addGenerated("M", 1000+int(seed))
	r.strangers = []*simNode{s.addGenerated("S1", 2000+int(seed)), s.addGenerated("S2", 3000+int(seed))}
	r.mod = newPropModel(t, s)
	switch seed % 4 {
	case 1:
		// A short list of requests makes the refusal for a full list common.
		for _, n := range r.nodes() {
			n.params.RevealBudget = 100
			n.params.PendingCap = 2
		}
		r.mod.budget = 100
	case 2:
		// A small budget makes the refusal for a spent budget common.
		for _, n := range r.nodes() {
			n.params.RevealBudget = 2
		}
		r.mod.budget = 2
	}
	return r
}

func (r *propRun) nodes() []*simNode { return append([]*simNode{r.a, r.b, r.m}, r.strangers...) }

func (r *propRun) pick() *simNode { ns := r.nodes(); return ns[r.rng.Intn(len(ns))] }

func (r *propRun) other(n *simNode) *simNode {
	for {
		if o := r.pick(); o != n {
			return o
		}
	}
}

func (r *propRun) step(faults bool) {
	s, rng := r.s, r.rng
	switch x := rng.Intn(100); {
	case x < 6: // somebody starts an online segment
		from := r.pick()
		to := r.other(from)
		if from == r.m && rng.Intn(2) == 0 {
			to = r.b // the attacker concentrates on B
		}
		in := Initiation{}
		if rng.Intn(3) == 0 {
			in.Target = to.party.node
		}
		_, out := s.initiate(from, to, in)
		r.cov["initiate/"+out.Result.String()]++
		if from == r.m && out.Result == ResultOK && rng.Intn(2) == 0 {
			// abort at the worst moment: after the target revealed
			for len(s.queue) > 0 && s.queue[0].toTarget {
				s.deliverOne(0, false)
			}
			for i := len(s.queue) - 1; i >= 0; i-- {
				if !s.queue[i].toTarget && !s.queue[i].closing {
					s.deliverOne(i, true)
					r.cov["attacker/abort after reveal"]++
				}
			}
		}
	case x < 32 && len(s.queue) > 0: // deliver, drop, duplicate or reorder a frame
		i := rng.Intn(len(s.queue))
		switch y := rng.Intn(100); {
		case faults && y < 12:
			r.cov["frame dropped"]++
			s.deliverOne(i, true)
		case faults && y < 20:
			r.cov["frame duplicated"]++
			d := s.queue[i]
			s.queue = append(s.queue, d)
			s.deliverOne(i, false)
		default:
			if i != 0 {
				r.cov["frame reordered"]++
			}
			s.deliverOne(i, false)
		}
	case x < 44 && len(s.fetches) > 0:
		i := rng.Intn(len(s.fetches))
		drop := faults && rng.Intn(4) == 0
		if drop {
			r.cov["fetch dropped"]++
		}
		s.runFetch(i, drop)
	case x < 54:
		s.tick()
	case x < 70:
		r.human(r.pick())
	case x < 71 && faults:
		n := r.pick()
		r.cov["crash"]++
		n.restart()
	case x < 72 && faults:
		// crash in the middle of the next action list of a node
		n := r.pick()
		n.crashAt = n.applied + 1 + rng.Intn(4)
	case x < 74:
		n := r.pick()
		o := r.other(n)
		if rng.Intn(4) == 0 {
			req := n.owner()
			next, out, actions, err := n.state.Forget(n.world(), req, o.party.node, s.now)
			must(r.t, err)
			n.state = next
			n.remember(out, req)
			n.apply(actions, nil)
		} else if out := n.expect(o); out.Result == ResultOK {
			r.cov["expectation entered"]++
		}
	case x < 75 && faults:
		n := r.pick()
		o := r.other(n)
		if _, revoked := n.view.LocallyRevoked[o.party.node]; revoked {
			delete(n.view.LocallyRevoked, o.party.node)
		} else if rng.Intn(4) == 0 {
			n.view.LocallyRevoked[o.party.node] = struct{}{}
			r.cov["locally revoked"]++
		}
	default:
		switch y := rng.Intn(100); {
		case y < 70:
			s.advance(time.Duration(rng.Intn(40)) * time.Second)
		case y < 95:
			s.advance(time.Duration(1+rng.Intn(10)) * time.Minute)
		default:
			s.advance(time.Duration(20+rng.Intn(50)) * time.Minute)
		}
	}
	r.mod.check()
	r.invariants()
}

// invariants are the structural properties that must hold after every step.
func (r *propRun) invariants() {
	for _, n := range r.nodes() {
		waiting := 0
		for _, rec := range n.state.Targets() {
			if rec.Waiting(r.s.now) && rec.Expected.IsZero() {
				waiting++
			}
		}
		if waiting > n.params.PendingCap {
			r.t.Fatalf("%s holds %d unexpected requests, over the cap", n.name, waiting)
		}
		if len(n.state.Expectations()) > n.params.ExpectationCap {
			r.t.Fatalf("%s holds too many expectations", n.name)
		}
	}
}

// human lets the owner of a node look at the requests it shows.
func (r *propRun) human(n *simNode) {
	rng := r.rng
	type pending struct {
		id   ID
		peer ident.NodeID
	}
	var list []pending
	for _, rec := range n.state.Targets() {
		if rec.Waiting(r.s.now) && rec.Confirmation == nil {
			list = append(list, pending{rec.ID(), rec.Transcript.terms.Initiator.node})
		}
	}
	for _, rec := range n.state.Initiators() {
		if rec.Status == InitiatorActive && rec.Confirmation == nil {
			list = append(list, pending{rec.ID(), rec.Transcript.terms.Target.node})
		}
	}
	// Records that wait for the owner, withdrawn or rejected on a whim.
	for _, p := range list {
		if rng.Intn(2) == 0 {
			continue
		}
		own := n.codeOf(p.id)
		peer := r.s.nodeByID(p.peer)
		switch y := rng.Intn(100); {
		case y < 82 && !r.hostile || y < 20:
			// Compare with the screen of the other node, if it shows this request.
			if peer == nil {
				continue
			}
			code, ok := peer.shownCode(p.id)
			if !ok {
				continue
			}
			out := n.confirm(p.id, code)
			r.cov["confirm/"+out.Result.String()]++
			if out.Result == ResultOK && code != own {
				r.t.Fatal("a confirmation succeeded with a code that is not the request's")
			}
		case y < 90 && !r.hostile || y < 30:
			out := n.confirm(p.id, (own+1+Code(rng.Intn(5)))%1000000)
			r.cov["confirm wrong code/"+out.Result.String()]++
			if out.Result == ResultOK {
				r.t.Fatal("a wrong code confirmed a request")
			}
		case y < 95 || r.hostile:
			out := n.reject(p.id, WithdrawMismatch)
			r.cov["reject mismatch/"+out.Result.String()]++
		default:
			out := n.reject(p.id, WithdrawDeclined)
			r.cov["reject declined/"+out.Result.String()]++
		}
	}
	// An owner may also withdraw a request this node has already signed.
	for _, rec := range n.state.Initiators() {
		if rec.Status == InitiatorActive && rec.Released && rng.Intn(25) == 0 {
			out := n.reject(rec.ID(), WithdrawDeclined)
			if out.Result == ResultOK {
				r.cov["withdrawn after release"]++
				d := oracleCredentialDigest(r.t, rec.Transcript)
				if _, ok := n.revocations[d]; !ok {
					r.t.Fatal("a withdrawn signature was not revoked at once")
				}
			}
		}
	}
}

// shownCode returns the code the node shows for a request, if it shows it.
func (n *simNode) shownCode(id ID) (Code, bool) {
	if r, ok := n.state.Target(id); ok {
		return r.Transcript.Code(), true
	}
	if r, ok := n.state.Initiator(id); ok {
		return r.Transcript.Code(), true
	}
	return 0, false
}

// finish stops the faults and lets everything end: every request expires or
// completes, every record's retention passes.
func (r *propRun) finish() {
	s := r.s
	s.dropFetch = nil
	for _, n := range r.nodes() {
		n.crashAt = 0
	}
	// Let in-flight work settle with a perfect network, humans idle.
	for i := 0; i < 400; i++ {
		s.advance(10 * time.Minute)
		s.tick()
		s.pump()
		r.mod.check()
	}
	r.mod.finalChecks(r.nodes()...)
}

func TestPropertyRandomSchedules(t *testing.T) {
	cov := map[string]int{}
	seeds := 300
	steps := 1500
	if testing.Short() {
		seeds, steps = 20, 800
	}
	for seed := int64(1); seed <= int64(seeds); seed++ {
		r := newPropRun(t, seed)
		for i := 0; i < steps; i++ {
			r.step(true)
		}
		r.finish()
		for k, v := range r.cov {
			cov[k] += v
		}
		for _, n := range r.nodes() {
			for _, no := range n.notices {
				switch no.Kind {
				case NoticeNotStarted:
					cov["notice/not started/"+no.Refusal.String()]++
				case NoticeAttemptUsed:
					cov["notice/attempt used"]++
				case NoticeCompleted:
					cov["notice/completed"]++
				case NoticeEnded:
					cov["notice/ended"]++
				case NoticeRevoked:
					cov["notice/revoked"]++
				}
			}
			cov["credentials stored"] += len(n.credentials)
			cov["revocations stored"] += len(n.revocations)
		}
		cov["m2 sent to B on behalf of the attacker"] += r.mod.m2sentToB
		cov["reveals spent"] += len(r.mod.spent["A"]) + len(r.mod.spent["B"]) + len(r.mod.spent["M"])
	}
	keys := make([]string, 0, len(cov))
	for k := range cov {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-50s %d", k, cov[k])
	}
	if testing.Short() {
		return // the shortened run is too small to reach every path; the full run asserts coverage
	}
	for _, k := range []string{
		"credentials stored", "revocations stored", "crash", "frame dropped", "frame duplicated", "frame reordered", "fetch dropped",
		"expectation entered", "locally revoked", "attacker/abort after reveal", "withdrawn after release", "reveals spent",
		"confirm/ok", "confirm wrong code/wrong_code", "notice/not started/paused",
		"notice/not started/not_accepted", "notice/attempt used", "notice/completed", "notice/ended", "notice/revoked", "reject mismatch/ok", "reject declined/ok",
		"initiate/ok", "initiate/in_progress",
	} {
		if cov[k] < 3 {
			t.Errorf("coverage: %q happened %d times", k, cov[k])
		}
	}
}

// TestCrashAtEveryPersistedStep runs one pairing between two nodes, over and
// over, with one node crashing before each possible action in turn (a persist,
// a stored credential, a frame), restarting from what it had persisted, and the
// owners acting again for as long as there is something to act on. Whatever the
// crash point, the outcome is one of two: both hold the same credential, or the
// initiator holds none and, if its signature ever left, has revoked it.
func TestCrashAtEveryPersistedStep(t *testing.T) {
	outcomes := map[string]int{}
	for _, crashNode := range []string{"A", "B"} {
		for point := 1; point <= 60; point++ {
			s, a, b := standardPair(t)
			mod := newPropModel(t, s)
			victim := a
			if crashNode == "B" {
				victim = b
			}
			victim.crashAt = point
			scriptedPairing(t, s, a, b, mod)
			s.dropFetch = nil
			for i := 0; i < 400; i++ {
				s.advance(10 * time.Minute)
				s.tick()
				s.pump()
				mod.check()
			}
			mod.finalChecks(a, b)
			outcomes[crashOutcome(a, b)]++
			if victim.crashAt != 0 {
				// The point lies beyond the last action of the run: nothing more to try.
				break
			}
		}
	}
	for k, v := range outcomes {
		t.Logf("%-55s %d", k, v)
	}
	if outcomes["completed on both sides"] < 5 || outcomes["ended without a credential"] < 5 {
		t.Fatalf("crash points did not reach both outcomes: %v", outcomes)
	}
}

func crashOutcome(a, b *simNode) string {
	switch {
	case len(a.credentials) > 0 && len(b.credentials) > 0:
		return "completed on both sides"
	case len(a.credentials) == 0 && len(b.credentials) > 0:
		return "ended without a credential (the target's was revoked)"
	case len(a.credentials)+len(b.credentials) == 0:
		return "ended without a credential"
	}
	return "inconsistent"
}

// scriptedPairing drives the owners of two nodes through a pairing as far as
// the (possibly crashing) nodes allow: they start it, compare the codes shown
// on both screens, confirm in the order target then initiator, and look again
// after each restart.
func scriptedPairing(t *testing.T, s *sim, a, b *simNode, mod *propModel) {
	_, out := s.initiate(a, b, Initiation{Target: b.party.node})
	if out.Result != ResultOK {
		t.Fatal(out)
	}
	for round := 0; round < 12; round++ {
		s.pump()
		s.tick()
		for _, n := range []*simNode{b, a} {
			for _, rec := range n.state.Targets() {
				if rec.Waiting(s.now) && rec.Confirmation == nil {
					n.confirm(rec.ID(), rec.Transcript.Code())
				}
			}
			for _, rec := range n.state.Initiators() {
				if rec.Status == InitiatorActive && rec.Confirmation == nil && now(s).Before(rec.ExpiresAt) {
					n.confirm(rec.ID(), rec.Transcript.Code())
				}
			}
		}
		s.advance(20 * time.Second)
		mod.check()
	}
}

func now(s *sim) time.Time { return s.now }
