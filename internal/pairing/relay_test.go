package pairing

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"testing"
	"time"
)

// The relay attack. A wants to pair with whoever answers its connection and
// reaches M instead (A names no target, as when it pairs with a node found on
// the local network); M answers A as the target and, in the same minutes,
// starts a pairing of its own with B as the initiator. A's owner reads a code
// off A's screen and B's owner reads one off B's screen; if the two match, the
// owners confirm and M holds a credential with each of them while both think
// they paired with each other.
//
// M's best strategy is to compute B's code offline as soon as B reveals its
// nonce, and to abort when the two codes differ. B's nonce is the one thing M
// cannot choose, and it arrives only after M committed, so each reveal of B
// gives M exactly one guess, which lands with probability 10^-6. The tests
// check what bounds M: B reveals to M at most its budget within any hour, M
// cannot open its commitment to anything but the nonce it committed to, and the
// codes on the two screens are equal only when the independent oracle says
// that the two transcripts collide.

// relayRound is what one attempt of M came to.
type relayRound struct {
	at time.Time
	// revealed is set when B revealed its nonce to M; otherwise B refused.
	revealed bool
	refusal  RefusalReason
	retry    time.Duration
	// codeA and codeB are the codes the oracle derives for the two sessions;
	// codeB is only known to M once B revealed.
	codeA, codeB Code
	// proceeded is set when M let its pairing with B go on to the opening.
	proceeded bool
}

type relay struct {
	t       *testing.T
	s       *sim
	a, m, b *simNode
	rounds  []relayRound
}

func newRelay(t *testing.T) *relay {
	s, a, b := standardPair(t)
	m := s.addGenerated("M", 3)
	// M is the attacker: the limits an honest target keeps do not bind it.
	m.params.RevealBudget = 1 << 20
	m.params.PendingCap = 1 << 10
	return &relay{t: t, s: s, a: a, m: m, b: b}
}

// activeInitiator is A's request toward M that still waits.
func (r *relay) activeInitiator() InitiatorRecord {
	for _, rec := range r.a.state.Initiators() {
		if rec.Status == InitiatorActive && rec.Transcript.terms.Target.node == r.m.party.node {
			return rec
		}
	}
	r.t.Fatal("A holds no request toward M")
	return InitiatorRecord{}
}

// attempt runs one round of the attack at the current time and records it.
func (r *relay) attempt() relayRound {
	t, s, a, m, b := r.t, r.s, r.a, r.m, r.b
	round := relayRound{at: s.now}

	// A's owner starts a pairing, and it reaches M, who answers it in full.
	if _, out := s.initiate(a, m, Initiation{}); out.Result != ResultOK {
		t.Fatalf("A could not start: %+v", out)
	}
	s.pump()
	ia := r.activeInitiator()
	_, round.codeA = oracle(t, ia.Transcript)
	if round.codeA != ia.Transcript.Code() {
		t.Fatalf("A shows %v, the oracle derives %v", ia.Transcript.Code(), round.codeA)
	}

	// M starts its own pairing with B and delivers the commitment.
	stB, out := s.initiate(m, b, Initiation{})
	if out.Result != ResultOK {
		t.Fatalf("M could not start toward B: %+v", out)
	}
	if len(s.queue) != 1 {
		t.Fatalf("%d frames queued after M's commitment", len(s.queue))
	}
	s.deliverOne(0, false)
	if len(s.queue) == 0 {
		t.Fatal("B did not answer")
	}
	frame := s.queue[0].frame

	if refusal, err := DecodeRefusal(frame); err == nil {
		round.refusal, round.retry = refusal.Reason, refusal.RetryAfter
		s.pump() // the refusal reaches M, and B closes the stream
		return r.finish(ia, round)
	}
	if len(s.queue) != 1 {
		t.Fatalf("B answered with %d frames", len(s.queue))
	}
	if _, err := DecodeReveal(frame); err != nil {
		t.Fatalf("B's answer is neither a reveal nor a refusal: %v", err)
	}
	round.revealed = true

	// M has B's nonce. Offline it computes B's code without sending anything:
	// the state its own machine would move to if it went on holds the
	// transcript, and the oracle derives the code from that.
	ahead, _, err := m.state.InitiatorReceive(m.world(), stB.initSID, frame, s.now)
	must(t, err)
	i := ahead.sessionIndex(stB.initSID)
	if i < 0 || ahead.sessions[i].transcript.IsZero() {
		t.Fatal("M's machine holds no transcript after a valid reveal")
	}
	_, round.codeB = oracle(t, ahead.sessions[i].transcript)

	if round.codeA == round.codeB {
		round.proceeded = true
		s.deliverOne(0, false) // the reveal reaches M's machine, which opens
		s.pump()
		return r.finish(ia, round)
	}

	// The codes differ: M drops the reveal, tries to open its commitment to
	// another nonce (one it could have picked to make the codes agree, if B
	// accepted it), and abandons the session.
	s.deliverOne(0, true)
	r.openAnotherNonce(stB, round)
	s.abandon(stB)
	return r.finish(ia, round)
}

// openAnotherNonce sends B an opening that is not the nonce M committed to. B
// must end the session and keep no request, and the session must stay ended.
func (r *relay) openAnotherNonce(st *simStream, round relayRound) {
	t, s, b, m := r.t, r.s, r.b, r.m
	other := nonceFromSeed("another opening", int(round.at.Unix()))
	frame, err := OpenMsg{NA: other}.Encode()
	must(t, err)
	next, actions, err := b.state.TargetReceive(b.world(), b.conn(m), st.tgtSID, frame, b.rng, s.now)
	must(t, err)
	for _, act := range actions {
		switch x := act.(type) {
		case PersistTarget:
			t.Fatalf("B kept a request for an opening that does not match the commitment: %+v", x)
		case Send:
			if _, err := DecodeAccepted(x.Frame); err == nil {
				t.Fatal("B accepted an opening that does not match the commitment")
			}
		}
	}
	b.state = next
	b.apply(actions, st)
	if _, held := b.pendingFrom(m); held {
		t.Fatal("B holds a request from M after a wrong opening")
	}
	// Having been refused once, M cannot come back on the same session, not
	// even with the nonce it committed to.
	committed := m.state.sessions[m.state.sessionIndex(st.initSID)].nA
	honest, err := OpenMsg{NA: committed}.Encode()
	must(t, err)
	_, again, err := b.state.TargetReceive(b.world(), b.conn(m), st.tgtSID, honest, b.rng, s.now)
	must(t, err)
	for _, act := range again {
		if _, persisted := act.(PersistTarget); persisted {
			t.Fatal("B kept a request after a second opening on a session it had ended")
		}
	}
}

// finish records the round and ends it the way the owners would: A's owner
// sees no matching request on B and declines, which is not a verification
// failure. A round M won is left standing for the caller to inspect.
func (r *relay) finish(ia InitiatorRecord, round relayRound) relayRound {
	r.rounds = append(r.rounds, round)
	if round.proceeded {
		return round
	}
	if out := r.a.reject(ia.ID(), WithdrawDeclined); out.Result != ResultOK {
		r.t.Fatalf("A's owner could not decline: %+v", out)
	}
	r.s.pump()
	return round
}

// abandon closes the initiator's end of a stream, as a peer that goes away.
func (s *sim) abandon(st *simStream) {
	n := st.initiator
	st.initClosed = true
	next, actions, err := n.state.InitiatorClosed(st.initSID)
	must(s.t, err)
	n.state = next
	n.apply(actions, st)
	s.queue = append(s.queue, simDelivery{stream: st, toTarget: true, closing: true})
	s.pump()
}

func TestRelayAttackerGetsOneGuessPerRevealAndNoMoreThanTheBudgetPerHour(t *testing.T) {
	r := newRelay(t)
	budget := DefaultParams().RevealBudget
	// An attempt every five minutes for just over two hours: far more than the
	// budget allows within the first hour.
	const attempts = 26
	for i := 0; i < attempts; i++ {
		r.attempt()
		r.s.advance(5 * time.Minute)
		r.s.tick()
	}

	var reveals []time.Time
	collisions, wins := 0, 0
	for i, round := range r.rounds {
		if round.proceeded {
			wins++
		}
		if round.revealed {
			reveals = append(reveals, round.at)
			if round.codeA == round.codeB {
				collisions++
			}
			if round.proceeded != (round.codeA == round.codeB) {
				t.Fatalf("round %d: M proceeded=%v with codes %v and %v", i, round.proceeded, round.codeA, round.codeB)
			}
			continue
		}
		if round.refusal != RefusalPaused || round.retry <= 0 {
			t.Fatalf("round %d: B refused with %v (retry %v), want paused with a hint", i, round.refusal, round.retry)
		}
		if round.proceeded || round.codeB != 0 {
			t.Fatalf("round %d: M got a guess from a refusal", i)
		}
	}

	t.Run("within any sliding hour B reveals to M at most its budget", func(t *testing.T) {
		if len(reveals) < budget+1 {
			t.Fatalf("only %d reveals over the run; the test must see the budget slide", len(reveals))
		}
		for i, at := range reveals {
			n := 0
			for _, other := range reveals[:i+1] {
				if at.Sub(other) < time.Hour {
					n++
				}
			}
			if n > budget {
				t.Fatalf("%d reveals in the hour ending at %v, budget %d", n, at, budget)
			}
		}
	})
	t.Run("the budget is used while it lasts and frees as the hour slides", func(t *testing.T) {
		// Attempts at minutes 0, 5, 10, 15 and 20 are answered, the next seven
		// are refused, and the one at minute 60 is answered because the first
		// reveal is exactly an hour old; then minute 65 frees the second.
		want := map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 12: true, 13: true}
		for i := 0; i < 14; i++ {
			if r.rounds[i].revealed != want[i] {
				t.Fatalf("attempt %d (minute %d): revealed=%v, want %v", i, 5*i, r.rounds[i].revealed, want[i])
			}
		}
	})
	t.Run("each reveal is one guess and a guess is a win only by collision", func(t *testing.T) {
		if wins != collisions {
			t.Fatalf("%d wins for %d collisions of the oracle's codes", wins, collisions)
		}
		if wins != 0 {
			t.Fatalf("M won %d of %d guesses with the fixed seeds; each should land with probability 10^-6", wins, len(reveals))
		}
	})
	t.Run("B holds nothing from M and counted every reveal itself", func(t *testing.T) {
		if _, held := r.b.pendingFrom(r.m); held {
			t.Fatal("B holds a request from M")
		}
		now := r.s.now
		inHour := 0
		for _, at := range reveals {
			if now.Sub(at) < time.Hour {
				inHour++
			}
		}
		counted := 0
		for _, at := range r.b.state.Limits().Reveals() {
			if now.Sub(at) < time.Hour {
				counted++
			}
		}
		if counted != inHour {
			t.Fatalf("B's counter holds %d reveals of the last hour, the attempts show %d", counted, inHour)
		}
	})
}

// scriptedReader supplies the given bytes first and then the fallback, so a
// test can choose the nonces of a node.
type scriptedReader struct {
	script   []byte
	fallback io.Reader
}

func (s *scriptedReader) Read(p []byte) (int, error) {
	if len(s.script) > 0 {
		n := copy(p, s.script)
		s.script = s.script[n:]
		return n, nil
	}
	return s.fallback.Read(p)
}

func nonceFromSeed(label string, i int) Nonce {
	return Nonce(sha256.Sum256(binary.BigEndian.AppendUint64([]byte(label), uint64(i))))
}

// When the two codes do collide, M gets through, at the price of the guess:
// the owners then see the same code on both screens. The collision is found
// the way an attacker would, by trying nonces for the two sessions until a code
// of one equals a code of the other, with the oracle as the only judge.
func TestRelayAttackerThatGuessesRightPassesOnlyByCollision(t *testing.T) {
	r := newRelay(t)
	s, a, m, b := r.s, r.a, r.m, r.b
	now := wholeNow(s.now)
	nA, nM := nonceFromSeed("nA", 0), nonceFromSeed("nM", 0)
	termsAM := Terms{Protocol: simProtocol, Initiator: a.party, Target: m.party, CreatedAt: now}
	termsMB := Terms{Protocol: simProtocol, Initiator: m.party, Target: b.party, CreatedAt: now}
	cA, err := termsAM.Commit(nA)
	must(t, err)
	cM, err := termsMB.Commit(nM)
	must(t, err)

	// M's answers to A give the candidates for A's code, B's nonces the
	// candidates for B's. With four thousand of the first, one B nonce in 250
	// is a hit.
	const many = 4000
	byCode := map[Code]Nonce{}
	for i := 0; i < many; i++ {
		nB := nonceFromSeed("nB toward A", i)
		_, code := oracleOf(t, termsAM, cA, nB, nA)
		byCode[code] = nB
	}
	var nMtoA, nBtoM Nonce
	for j := 0; ; j++ {
		if j > 2_000_000 {
			t.Fatal("no collision found")
		}
		nB := nonceFromSeed("nB toward M", j)
		_, code := oracleOf(t, termsMB, cM, nB, nM)
		if n, ok := byCode[code]; ok {
			nMtoA, nBtoM = n, nB
			break
		}
	}

	a.rng = &scriptedReader{script: nA[:], fallback: a.rng}
	m.rng = &scriptedReader{script: append(append([]byte(nil), nMtoA[:]...), nM[:]...), fallback: m.rng}
	b.rng = &scriptedReader{script: nBtoM[:], fallback: b.rng}

	round := r.attempt()
	if !round.revealed || !round.proceeded || round.codeA != round.codeB {
		t.Fatalf("round %+v: the colliding nonces did not let M through", round)
	}
	ia := r.activeInitiator()
	tb, held := b.pendingFrom(m)
	if !held {
		t.Fatal("B holds no request from M after the opening")
	}
	if _, codeOnB := oracle(t, tb.Transcript); codeOnB != round.codeA || tb.Transcript.Code() != ia.Transcript.Code() {
		t.Fatalf("screens show %v on A and %v on B; the oracle derives %v and %v", ia.Transcript.Code(), tb.Transcript.Code(), round.codeA, codeOnB)
	}
	if got := len(b.state.Limits().Reveals()); got != 1 {
		t.Fatalf("the win cost %d reveals of B, want 1", got)
	}
}
