package pairing

import (
	"bytes"
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

// startPairing runs the online segment from a to b and returns the two ends'
// records.
func startPairing(t testing.TB, s *sim, a, b *simNode) (InitiatorRecord, TargetRecord) {
	t.Helper()
	_, out := s.initiate(a, b, Initiation{Target: b.party.node})
	if out.Disposition != DispositionExecuted || out.Result != ResultOK {
		t.Fatalf("initiate: %+v", out)
	}
	s.pump()
	ia, ok := a.initiatedTo(b)
	if !ok {
		t.Fatalf("no initiator record; notices: %+v", a.notices)
	}
	tb, ok := b.pendingFrom(a)
	if !ok {
		t.Fatal("no target record")
	}
	return ia, tb
}

func TestHappyPathInitiatorConfirmsFirst(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	if ia.ID() != tb.ID() {
		t.Fatal("the two sides computed different pairing identifiers")
	}
	// Both screens show the same code, which an independent derivation agrees with.
	id, code := oracle(t, ia.Transcript)
	if id != ia.ID() || code != ia.Transcript.Code() || code != tb.Transcript.Code() {
		t.Fatalf("code/id disagree with the oracle: %v %v %v", code, ia.Transcript.Code(), tb.Transcript.Code())
	}
	if out := a.confirm(ia.ID(), code); out.Result != ResultOK {
		t.Fatalf("A confirm: %+v", out)
	}
	s.settle()
	if got, _ := b.state.Target(tb.ID()); got.InitiatorSignature == nil || got.Status != TargetPending {
		t.Fatalf("B should hold A's signature and wait: %+v", got.Status)
	}
	if len(b.credentials) != 0 || len(a.credentials) != 0 {
		t.Fatal("a credential exists before both confirmed")
	}
	if out := b.confirm(tb.ID(), code); out.Result != ResultOK {
		t.Fatalf("B confirm: %+v", out)
	}
	s.run(2*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
}

func TestHappyPathTargetConfirmsFirst(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	code := tb.Transcript.Code()
	if out := b.confirm(tb.ID(), code); out.Result != ResultOK {
		t.Fatalf("B confirm: %+v", out)
	}
	s.settle()
	if len(b.credentials) != 0 {
		t.Fatal("B signed before it had the initiator's signature")
	}
	if out := a.confirm(ia.ID(), code); out.Result != ResultOK {
		t.Fatalf("A confirm: %+v", out)
	}
	s.run(2*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
}

func assertCompleted(t testing.TB, a, b *simNode, id ID) {
	t.Helper()
	if len(a.credentials) != 1 || len(b.credentials) != 1 {
		t.Fatalf("credentials: A %d, B %d; A notices %+v", len(a.credentials), len(b.credentials), a.notices)
	}
	var ca, cb record.Record
	for _, r := range a.credentials {
		ca = r
	}
	for _, r := range b.credentials {
		cb = r
	}
	ea, _ := ca.Encode()
	eb, _ := cb.Encode()
	if !bytes.Equal(ea, eb) {
		t.Fatal("the two sides hold different credentials")
	}
	ver, err := record.Verify(credential.Descriptor, ca)
	if err != nil {
		t.Fatal(err)
	}
	if ver.Payload().A == ver.Payload().B {
		t.Fatal("degenerate credential")
	}
	if r, _ := a.state.Initiator(id); r.Status != InitiatorComplete {
		t.Fatalf("initiator record is %v", r.Status)
	}
	if r, _ := b.state.Target(id); r.Status != TargetComplete {
		t.Fatalf("target record is %v", r.Status)
	}
	if len(a.revocations) != 0 {
		t.Fatal("a completed pairing was revoked")
	}
	_ = time.Second
}

func TestWrongCodeIsNotAFailure(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	wrong := (tb.Transcript.Code() + 1) % 1000000
	out := b.confirm(tb.ID(), wrong)
	if out.Disposition != DispositionExecuted || out.Result != ResultWrongCode {
		t.Fatalf("outcome %+v", out)
	}
	if n := len(b.state.Limits().Failures()); n != 0 {
		t.Fatalf("a wrong code was counted as %d failures", n)
	}
	if r, _ := b.state.Target(tb.ID()); r.Confirmation != nil {
		t.Fatal("a wrong code confirmed the request")
	}
	// Any number of wrong entries, then the right one.
	for i := 0; i < 10; i++ {
		b.confirm(tb.ID(), wrong)
	}
	if out := b.confirm(tb.ID(), tb.Transcript.Code()); out.Result != ResultOK {
		t.Fatalf("right code after wrong ones: %+v", out)
	}
	_ = ia
}

func TestMismatchCountsAndThirdFailurePauses(t *testing.T) {
	s, a, b := standardPair(t)
	b.params.RevealBudget = 100
	var initiators []*simNode
	for i := 0; i < 5; i++ {
		initiators = append(initiators, s.addGenerated("C", 10+i))
	}
	_ = a
	reject := func(c *simNode, mismatch bool) {
		t.Helper()
		_, out := s.initiate(c, b, Initiation{})
		if out.Result != ResultOK {
			t.Fatalf("initiate: %+v", out)
		}
		s.pump()
		rec, ok := b.pendingFrom(c)
		if !ok {
			t.Fatalf("no pending request from %s; notices %+v", c.name, c.notices)
		}
		reason := WithdrawDeclined
		if mismatch {
			reason = WithdrawMismatch
		}
		if out := b.reject(rec.ID(), reason); out.Result != ResultOK {
			t.Fatalf("reject: %+v", out)
		}
	}
	t.Run("declining is not a failure", func(t *testing.T) {
		reject(initiators[0], false)
		if n := len(b.state.Limits().Failures()); n != 0 {
			t.Fatalf("declining counted as %d failures", n)
		}
	})
	reject(initiators[1], true)
	s.advance(10 * time.Minute)
	reject(initiators[2], true)
	if paused, _ := b.state.Limits().Paused(b.params, s.now); paused {
		t.Fatal("paused after two failures")
	}
	s.advance(10 * time.Minute)
	reject(initiators[3], true)
	paused, retry := b.state.Limits().Paused(b.params, s.now)
	if !paused || retry != 40*time.Minute {
		t.Fatalf("after the third failure: paused=%v retry=%v, want paused for 40m", paused, retry)
	}

	t.Run("a new request from an unexpected node is refused as paused", func(t *testing.T) {
		d := initiators[4]
		_, out := s.initiate(d, b, Initiation{})
		s.pump()
		if out.Result != ResultOK {
			t.Fatalf("initiator side: %+v", out)
		}
		if len(d.notices) != 1 || d.notices[0].Kind != NoticeNotStarted || d.notices[0].Refusal != RefusalPaused || d.notices[0].RetryAfter != 40*time.Minute {
			t.Fatalf("notices %+v", d.notices)
		}
		if _, ok := b.pendingFrom(d); ok {
			t.Fatal("a request was accepted during the pause")
		}
	})
	t.Run("the initiator side is paused too", func(t *testing.T) {
		_, out := s.initiate(b, a, Initiation{})
		if out.Result != ResultPaused || out.RetryAfter != 40*time.Minute {
			t.Fatalf("initiate during the pause: %+v", out)
		}
	})
	t.Run("an expected node is not affected", func(t *testing.T) {
		if out := b.expect(a); out.Result != ResultOK {
			t.Fatalf("expect: %+v", out)
		}
		ia, tb := startPairing(t, s, a, b)
		if tb.Expected.IsZero() || tb.Confirmation == nil || tb.Confirmation.Method != ConfirmedByExpectation {
			t.Fatalf("expected request not pre-confirmed: %+v", tb)
		}
		_ = ia
	})
	t.Run("the pause lifts by itself", func(t *testing.T) {
		s.advance(41 * time.Minute)
		if paused, _ := b.state.Limits().Paused(b.params, s.now); paused {
			t.Fatal("still paused")
		}
		_, out := s.initiate(initiators[4], b, Initiation{})
		s.pump()
		if out.Result != ResultOK {
			t.Fatal(out)
		}
		if _, ok := b.pendingFrom(initiators[4]); !ok {
			t.Fatalf("no request after the pause; notices %+v", initiators[4].notices)
		}
	})
}

// dropReveal runs an online segment up to the target's reveal and drops it, so
// the target spent a reveal and the initiator never sees it.
func dropReveal(t testing.TB, s *sim, from, to *simNode) Outcome {
	t.Helper()
	s.pump()
	_, out := s.initiate(from, to, Initiation{})
	if out.Result != ResultOK {
		return out
	}
	s.deliverOne(0, false) // M1 reaches the target
	for len(s.queue) > 0 {
		s.deliverOne(0, true) // everything the target sent back is lost
	}
	return out
}

func TestRevealBudgetBoundsAbortAndRetry(t *testing.T) {
	s, a, b := standardPair(t)
	first := s.now
	for i := 0; i < 5; i++ {
		if out := dropReveal(t, s, a, b); out.Result != ResultOK {
			t.Fatalf("attempt %d: %+v", i, out)
		}
		s.advance(31 * time.Second)
		s.tick()
	}
	if got := len(b.state.Limits().Reveals()); got != 5 {
		t.Fatalf("target spent %d reveals, want 5", got)
	}
	// The sixth attempt within the hour gets no reveal.
	_, out := s.initiate(a, b, Initiation{})
	if out.Result != ResultOK {
		t.Fatal(out)
	}
	s.pump()
	last := a.notices[len(a.notices)-1]
	wantRetry := first.Add(time.Hour).Sub(s.now)
	if last.Kind != NoticeNotStarted || last.Refusal != RefusalPaused || last.RetryAfter != wantRetry.Round(time.Second) {
		t.Fatalf("notice %+v, want paused with retry %v", last, wantRetry)
	}
	if got := len(b.state.Limits().Reveals()); got != 5 {
		t.Fatalf("a refused request spent a reveal: %d", got)
	}
	// An hour after the first reveal the budget has a slot again.
	s.advance(wantRetry)
	s.tick()
	if out := dropReveal(t, s, a, b); out.Result != ResultOK {
		t.Fatal(out)
	}
	if got := len(b.state.Limits().Reveals()); got != 5 {
		t.Fatalf("reveals kept = %d, want 5 (the first slid out)", got)
	}
}

// The target's clock stepping back, even by days, neither corrupts the counters
// it persists nor makes it send a refusal the initiator cannot read.
func TestRevealBudgetAcrossTheClockSteppingBack(t *testing.T) {
	s, a, b := standardPair(t)
	for i := 0; i < 2; i++ {
		if out := dropReveal(t, s, a, b); out.Result != ResultOK {
			t.Fatalf("attempt %d: %+v", i, out)
		}
		s.advance(31 * time.Second)
		s.tick()
	}
	s.advance(-2 * time.Second)
	if out := dropReveal(t, s, a, b); out.Result != ResultOK {
		t.Fatalf("attempt after the step back: %+v", out)
	}
	b.restart() // the persisted counters must read back
	if got := len(b.state.Limits().Reveals()); got != 3 {
		t.Fatalf("after a restart the target kept %d reveals, want 3", got)
	}
	for i := 0; i < 2; i++ {
		s.advance(31 * time.Second)
		s.tick()
		if out := dropReveal(t, s, a, b); out.Result != ResultOK {
			t.Fatalf("attempt %d: %+v", i+3, out)
		}
	}
	s.advance(31 * time.Second)
	s.tick()
	s.advance(-3 * 24 * time.Hour)
	_, out := s.initiate(a, b, Initiation{})
	if out.Result != ResultOK {
		t.Fatal(out)
	}
	s.pump()
	last := a.notices[len(a.notices)-1]
	if last.Kind != NoticeNotStarted || last.Refusal != RefusalPaused || last.RetryAfter != MaxRetryAfter {
		t.Fatalf("notice %+v, want paused with the retry hint clamped to %v", last, MaxRetryAfter)
	}
}

func TestExpectedInitiatorSpendsNoReveal(t *testing.T) {
	s, a, b := standardPair(t)
	b.expect(a)
	for i := 0; i < 8; i++ {
		if out := dropReveal(t, s, a, b); out.Result != ResultOK {
			t.Fatalf("attempt %d: %+v", i, out)
		}
		s.advance(31 * time.Second)
		s.tick()
		if len(b.state.Expectations()) != 1 {
			t.Fatal("an expectation was used up by an aborted segment")
		}
	}
	if got := len(b.state.Limits().Reveals()); got != 0 {
		t.Fatalf("expected initiator spent %d reveals", got)
	}
}

func TestPendingCapHasNoEviction(t *testing.T) {
	s, a, b := standardPair(t)
	b.params.RevealBudget = 1000
	var firstID ID
	for i := 0; i < 16; i++ {
		c := s.addGenerated("C", 20+i)
		_, out := s.initiate(c, b, Initiation{})
		if out.Result != ResultOK {
			t.Fatal(out)
		}
		s.pump()
		rec, ok := b.pendingFrom(c)
		if !ok {
			t.Fatalf("request %d not accepted; notices %+v", i, c.notices)
		}
		if i == 0 {
			firstID = rec.ID()
		}
	}
	_, out := s.initiate(a, b, Initiation{})
	if out.Result != ResultOK {
		t.Fatal(out)
	}
	s.pump()
	if n := a.notices[len(a.notices)-1]; n.Kind != NoticeNotStarted || n.Refusal != RefusalBusy {
		t.Fatalf("17th request: %+v", n)
	}
	if _, ok := b.state.Target(firstID); !ok || len(b.state.Targets()) != 16 {
		t.Fatalf("a request was evicted: %d held", len(b.state.Targets()))
	}
	t.Run("an expected initiator does not count and is not refused", func(t *testing.T) {
		b.expect(a)
		if _, tb := startPairing(t, s, a, b); tb.Expected.IsZero() {
			t.Fatal("not expected")
		}
		if len(b.state.Targets()) != 17 {
			t.Fatalf("held %d requests", len(b.state.Targets()))
		}
	})
	t.Run("a finished request frees its slot", func(t *testing.T) {
		b.reject(firstID, WithdrawDeclined)
		c := s.addGenerated("C", 99)
		s.initiate(c, b, Initiation{})
		s.pump()
		if _, ok := b.pendingFrom(c); !ok {
			t.Fatalf("no slot after a rejection; notices %+v", c.notices)
		}
	})
}

func TestSameInitiatorReplacesAnOrphanButNotASignedRequest(t *testing.T) {
	s, a, b := standardPair(t)
	_, first := startPairing(t, s, a, b)
	// A crashes after M4, before its record was saved: B's request is an orphan.
	for k := range a.disk {
		delete(a.disk, k)
	}
	a.restart()
	// B's owner had even confirmed it already.
	if out := b.confirm(first.ID(), first.Transcript.Code()); out.Result != ResultOK {
		t.Fatal(out)
	}
	s.advance(time.Minute)
	ia, second := startPairing(t, s, a, b)
	if second.ID() == first.ID() {
		t.Fatal("two requests share an identifier")
	}
	if _, ok := b.state.Target(first.ID()); ok {
		t.Fatal("the orphan was not replaced")
	}
	if len(b.state.Targets()) != 1 {
		t.Fatalf("B holds %d requests", len(b.state.Targets()))
	}
	if _, onDisk := b.disk["target/"+hexID(first.ID())]; onDisk {
		t.Fatal("the orphan is still on disk")
	}
	// Now A's request holds its signature; a newer one is refused as busy.
	a.confirm(ia.ID(), second.Transcript.Code())
	s.settle()
	if r, _ := b.state.Target(second.ID()); r.InitiatorSignature == nil {
		t.Fatal("B does not hold A's signature")
	}
	// A second initiation needs the first to be gone from A's side; withdraw it
	// there first, which tells B... so test B's rule directly with a new session.
	_ = ia
	st := &simStream{id: 99, initiator: a, target: b, initSID: 900, tgtSID: 901}
	s.streams = append(s.streams, st)
	next, _, err := b.state.TargetOpen(b.world(), st.tgtSID, s.now)
	must(t, err)
	b.state = next
	terms := Terms{Protocol: simProtocol, Initiator: a.party, Target: b.party, CreatedAt: wholeNow(s.now)}
	commit, err := terms.Commit(nonceOf(5))
	must(t, err)
	m1, _ := CommitMsg{Commitment: commit, CreatedAt: terms.CreatedAt}.Encode()
	next, actions, err := b.state.TargetReceive(b.world(), b.conn(a), st.tgtSID, m1, b.rng, s.now)
	must(t, err)
	b.state = next
	refusal := firstSend(t, actions)
	r, err := DecodeRefusal(refusal)
	if err != nil || r.Reason != RefusalBusy {
		t.Fatalf("refusal %+v %v", r, err)
	}
}

func hexID(id ID) string { return fmtHex(id[:]) }

func fmtHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

func firstSend(t testing.TB, actions []Action) []byte {
	t.Helper()
	for _, a := range actions {
		if s, ok := a.(Send); ok {
			return s.Frame
		}
	}
	t.Fatalf("no frame in %+v", actions)
	return nil
}

// A pairing nobody confirms expires on both sides by their own clocks; an
// initiator that signed learns it and revokes.
func TestUnconfirmedRequestExpires(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	s.run(61*time.Minute, time.Minute)
	if out := b.confirm(tb.ID(), tb.Transcript.Code()); out.Result != ResultExpired {
		t.Fatalf("confirming an expired request: %+v", out)
	}
	rb, _ := b.state.Target(tb.ID())
	if rb.Status != TargetExpired {
		t.Fatalf("B record: %v", rb.Status)
	}
	ra, _ := a.state.Initiator(ia.ID())
	if ra.Status != InitiatorExpired {
		t.Fatalf("A record: %v", ra.Status)
	}
	if len(a.revocations) != 0 {
		t.Fatal("A never signed and must not revoke")
	}
	if len(a.credentials)+len(b.credentials) != 0 {
		t.Fatal("a credential came out of an expired request")
	}
}

func TestSignedRequestThatExpiresIsRevokedByTheInitiator(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(2*time.Minute, 10*time.Second)
	if r, _ := a.state.Initiator(ia.ID()); !r.Released {
		t.Fatal("the signature never left A")
	}
	s.run(62*time.Minute, time.Minute)
	ra, _ := a.state.Initiator(ia.ID())
	if ra.Status != InitiatorExpired {
		t.Fatalf("A record %v", ra.Status)
	}
	if len(a.revocations) != 1 {
		t.Fatalf("A stored %d revocations, want 1", len(a.revocations))
	}
	digest, _ := ia.Transcript.Credential().Digest()
	if _, ok := a.revocations[digest]; !ok {
		t.Fatal("the revocation is not for the credential A signed")
	}
	if !a.state.Withdrawn(digest) {
		t.Fatal("A does not refuse the credential it revoked")
	}
	if len(b.credentials) != 0 {
		t.Fatal("B completed an expired request")
	}
	_ = tb
}

func TestOwnerWithdrawsAfterSigning(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(30*time.Second, 5*time.Second)
	if out := a.reject(ia.ID(), WithdrawDeclined); out.Result != ResultOK {
		t.Fatal(out)
	}
	// The revocation is stored at once, before the target has been told.
	if len(a.revocations) != 1 {
		t.Fatalf("revocations stored at withdrawal: %d", len(a.revocations))
	}
	s.run(2*time.Minute, 10*time.Second)
	if r, _ := b.state.Target(tb.ID()); r.Status != TargetCancelled {
		t.Fatalf("B record %v", r.Status)
	}
	if r, _ := a.state.Initiator(ia.ID()); r.Status != InitiatorWithdrawn {
		t.Fatalf("A record %v", r.Status)
	}
	// B's owner can no longer confirm it.
	if out := b.confirm(tb.ID(), tb.Transcript.Code()); out.Result != ResultWrongState {
		t.Fatalf("confirm after cancel: %+v", out)
	}
	if len(b.credentials) != 0 {
		t.Fatal("a withdrawn pairing completed")
	}
}

// A hostile target that completes the credential and keeps it back cannot make
// the withdrawn signature count: the initiator revoked it without waiting.
func TestWithdrawnSignatureIsRevokedEvenIfTargetCompletesLater(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.confirm(tb.ID(), tb.Transcript.Code())
	a.confirm(ia.ID(), ia.Transcript.Code())
	// A's signature reaches B, which completes; A never hears back.
	s.dropFetch = func(simFetch) bool { return false }
	s.tick()
	// deliver the exchange to B but lose the answer: run it by hand.
	f := s.fetches[0]
	s.fetches = s.fetches[1:]
	to := b
	tsid := to.nextSID
	to.nextSID++
	st := &simStream{initiator: a, target: to, tgtSID: tsid}
	s.streams = append(s.streams, st)
	next, _, err := to.state.TargetOpen(to.world(), tsid, s.now)
	must(t, err)
	to.state = next
	next, actions, err := to.state.TargetReceive(to.world(), to.conn(a), tsid, f.frame, to.rng, s.now)
	must(t, err)
	to.state = next
	to.apply(actions, st)
	if len(b.credentials) != 1 {
		t.Fatalf("B did not complete: %d credentials", len(b.credentials))
	}
	next, acts, err := a.state.OnFetchFailed(a.world(), f.id, a.rng, s.now)
	must(t, err)
	a.state = next
	a.apply(acts, nil)
	// A's owner withdraws.
	a.reject(ia.ID(), WithdrawDeclined)
	digest, _ := ia.Transcript.Credential().Digest()
	if _, ok := a.revocations[digest]; !ok {
		t.Fatal("A did not revoke the credential B holds")
	}
	// B's credential, synced to A later, is refused.
	if !a.state.Withdrawn(digest) {
		t.Fatal("A would accept the credential it withdrew")
	}
	s.run(3*time.Minute, 10*time.Second)
	if r, _ := a.state.Initiator(ia.ID()); r.Status != InitiatorWithdrawn {
		t.Fatalf("A record %v", r.Status)
	}
	if len(a.credentials) != 0 {
		t.Fatal("A stored the credential it had withdrawn")
	}
}

func TestInitiatorBehindNATFetchesTheCredentialLater(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(10*time.Second, time.Second) // A's signature reaches B
	offline := true
	s.dropFetch = func(f simFetch) bool { return offline && f.from == a }
	s.run(5*time.Minute, 10*time.Second)
	b.confirm(tb.ID(), tb.Transcript.Code()) // B completes while A cannot be reached
	if len(b.credentials) != 1 || len(a.credentials) != 0 {
		t.Fatalf("B %d, A %d credentials", len(b.credentials), len(a.credentials))
	}
	s.run(100*time.Minute, time.Minute) // far beyond the request's own time to live
	if len(a.credentials) != 0 {
		t.Fatal("A fetched while offline")
	}
	offline = false
	s.run(3*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
	if len(a.revocations) != 0 {
		t.Fatal("A revoked a pairing that completed")
	}
}

func TestOnlineSegmentEndsAtTheTimeLimit(t *testing.T) {
	s, a, b := standardPair(t)
	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false) // M1
	for len(s.queue) > 0 {
		s.deliverOne(0, true)
	}
	s.advance(29 * time.Second)
	s.tick()
	if len(a.notices) != 0 {
		t.Fatalf("segment ended early: %+v", a.notices)
	}
	s.advance(2 * time.Second)
	s.tick()
	if len(a.notices) != 1 || a.notices[0].Kind != NoticeNotStarted {
		t.Fatalf("A notices %+v", a.notices)
	}
	if len(b.state.sessions) != 0 || len(a.state.sessions) != 0 {
		t.Fatal("sessions linger past the limit")
	}
	if len(b.state.Targets()) != 0 {
		t.Fatal("a request exists after a timed-out segment")
	}
}

func TestLateOpenIsRefusedByTheTarget(t *testing.T) {
	s, a, b := standardPair(t)
	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false) // M1 -> M2 queued
	s.deliverOne(0, false) // M2 -> M3 queued
	s.advance(31 * time.Second)
	for len(s.queue) > 0 {
		s.deliverOne(0, false)
	}
	if len(b.state.Targets()) != 0 {
		t.Fatal("a late open created a request")
	}
	if len(a.notices) != 0 && a.notices[len(a.notices)-1].Kind == NoticeCodeReady {
		t.Fatal("A got an acceptance")
	}
}

// A valid reveal must be read before the initiator writes its open.
func TestInitiatorWritesNothingBeforeAValidReveal(t *testing.T) {
	s, a, b := standardPair(t)
	s.initiate(a, b, Initiation{})
	s.pump() // runs the whole online segment; start again for the garbage tests
	c := s.addGenerated("C", 1)
	garbage := map[string][]byte{
		"empty":          nil,
		"random bytes":   {1, 2, 3},
		"an M3":          mustEncode(t, OpenMsg{NA: nonceOf(1)}),
		"an M4":          mustEncode(t, AcceptedMsg{ID: ID{1}, ExpiresAt: s.now.Add(time.Hour)}),
		"a result":       mustEncode(t, ResultMsg{ID: ID{1}, Status: StatusUnknown}),
		"a zero reveal":  mustMarshal(t, wrap(TypeReveal, revealWire{NB: make([]byte, 32)})),
		"a short reveal": mustMarshal(t, wrap(TypeReveal, revealWire{NB: make([]byte, 31)})),
	}
	for name, frame := range garbage {
		t.Run(name, func(t *testing.T) {
			sid := SessionID(500 + len(name))
			next, out, _, err := a.state.Initiate(a.world(), a.conn(c), sid, a.owner(), Initiation{}, a.rng, s.now)
			must(t, err)
			if out.Result != ResultOK {
				t.Fatal(out)
			}
			next, actions, err := next.InitiatorReceive(a.world(), sid, frame, s.now)
			must(t, err)
			for _, act := range actions {
				if _, sent := act.(Send); sent {
					t.Fatalf("the initiator wrote %T after %s", act, name)
				}
			}
			if i := next.sessionIndex(sid); i >= 0 {
				t.Fatal("the segment is still open")
			}
		})
	}
	_ = b
}

func mustEncode(t testing.TB, m interface{ Encode() ([]byte, error) }) []byte {
	t.Helper()
	b, err := m.Encode()
	must(t, err)
	return b
}

func mustMarshal(t testing.TB, v any) []byte {
	t.Helper()
	b, err := record.MarshalCanonical(v)
	must(t, err)
	return b
}

// refusalFor opens a stream from a to b with a hand-made commitment and returns
// what b answers.
func refusalFor(t testing.TB, s *sim, from, to *simNode, createdAt time.Time) RefusalMsg {
	t.Helper()
	terms := Terms{Protocol: simProtocol, Initiator: from.party, Target: to.party, CreatedAt: wholeNow(createdAt)}
	commit, err := terms.Commit(nonceOf(9))
	must(t, err)
	m1, _ := CommitMsg{Commitment: commit, CreatedAt: terms.CreatedAt}.Encode()
	sid := to.nextSID
	to.nextSID++
	next, _, err := to.state.TargetOpen(to.world(), sid, s.now)
	must(t, err)
	to.state = next
	next, actions, err := to.state.TargetReceive(to.world(), to.conn(from), sid, m1, to.rng, s.now)
	must(t, err)
	to.state = next
	for _, a := range actions {
		if snd, ok := a.(Send); ok {
			if r, err := DecodeRefusal(snd.Frame); err == nil {
				return r
			}
			t.Fatalf("answered with something else than a refusal")
		}
	}
	t.Fatal("no answer")
	return RefusalMsg{}
}

func TestTargetRefusals(t *testing.T) {
	setups := []struct {
		name  string
		setup func(s *sim, a, b *simNode) time.Time
		want  RefusalReason
	}{
		{"clock ahead beyond the tolerance", func(s *sim, a, b *simNode) time.Time { return s.now.Add(3 * time.Minute) }, RefusalClockSkew},
		{"clock behind beyond the tolerance", func(s *sim, a, b *simNode) time.Time { return s.now.Add(-3 * time.Minute) }, RefusalClockSkew},
		{"retired node", func(s *sim, a, b *simNode) time.Time {
			node, _ := ident.NodeIDFromKey(signerFromSeed(t, seedTest1).Public())
			_ = node
			rev, err := revocation.Verify(mustRetire(t, seedTest1))
			must(t, err)
			must(t, b.retired.Add(rev))
			return s.now
		}, RefusalNotAccepted},
		{"locally revoked node", func(s *sim, a, b *simNode) time.Time {
			b.view.LocallyRevoked[a.party.node] = struct{}{}
			return s.now
		}, RefusalNotAccepted},
		{"already a direct credential", func(s *sim, a, b *simNode) time.Time {
			b.view.Credentials = append(b.view.Credentials, trust.Credential{Digest: trust.CredentialDigest{9}, A: a.party.node, B: b.party.node})
			return s.now
		}, RefusalNotAccepted},
	}
	for _, tt := range setups {
		t.Run(tt.name, func(t *testing.T) {
			s, a, b := standardPair(t)
			created := tt.setup(s, a, b)
			if got := refusalFor(t, s, a, b, created); got.Reason != tt.want || got.RetryAfter != 0 {
				t.Fatalf("refusal %+v, want %v", got, tt.want)
			}
		})
	}
	t.Run("the node itself", func(t *testing.T) {
		s, _, b := standardPair(t)
		sid := b.nextSID
		next, _, err := b.state.TargetOpen(b.world(), sid, s.now)
		must(t, err)
		b.state = next
		m1 := mustEncode(t, CommitMsg{Commitment: Commitment{1}, CreatedAt: wholeNow(s.now)})
		_, actions, err := b.state.TargetReceive(b.world(), b.conn(b), sid, m1, b.rng, s.now)
		must(t, err)
		if got, err := DecodeRefusal(firstSend(t, actions)); err != nil || got.Reason != RefusalNotAccepted {
			t.Fatalf("refusal %+v %v", got, err)
		}
	})
	t.Run("reasons that must look alike do not differ", func(t *testing.T) {
		// retired, locally revoked, already trusted: the same bytes.
		var frames [][]byte
		for _, mk := range []func(a, b *simNode){
			func(a, b *simNode) { b.view.LocallyRevoked[a.party.node] = struct{}{} },
			func(a, b *simNode) {
				b.view.Credentials = append(b.view.Credentials, trust.Credential{Digest: trust.CredentialDigest{9}, A: a.party.node, B: b.party.node})
			},
		} {
			s, a, b := standardPair(t)
			mk(a, b)
			r := refusalFor(t, s, a, b, s.now)
			frames = append(frames, mustEncode(t, r))
		}
		if !bytes.Equal(frames[0], frames[1]) {
			t.Fatal("two causes are distinguishable to the stranger")
		}
	})
}

func mustRetire(t testing.TB, seed string) record.Record {
	t.Helper()
	r, err := revocation.IssueNodeSelfRevocation(signerFromSeed(t, seed))
	must(t, err)
	return r
}

// Whoever is not the initiator of a request gets exactly the answer for a
// request that does not exist.
func TestFetchByAnyoneElseLooksLikeNoSuchRequest(t *testing.T) {
	s, a, b := standardPair(t)
	_, tb := startPairing(t, s, a, b)
	c := s.addGenerated("C", 3)
	ask := func(from *simNode, id ID) []byte {
		t.Helper()
		frame := mustEncode(t, FetchMsg{ID: id})
		sid := b.nextSID
		b.nextSID++
		next, _, err := b.state.TargetOpen(b.world(), sid, s.now)
		must(t, err)
		b.state = next
		next, actions, err := b.state.TargetReceive(b.world(), b.conn(from), sid, frame, b.rng, s.now)
		must(t, err)
		b.state = next
		b.apply(actions, nil)
		reply, err := DecodeResult(firstSend(t, actions))
		must(t, err)
		return mustEncode(t, reply)
	}
	stranger := ask(c, tb.ID())
	missing := ask(c, ID{7, 7, 7})
	if want := mustEncode(t, ResultMsg{ID: tb.ID(), Status: StatusUnknown}); !bytes.Equal(stranger, want) {
		t.Fatalf("stranger got %x", stranger)
	}
	// Same shape, with its own id echoed: nothing else differs.
	if len(stranger) != len(missing) {
		t.Fatal("the answers differ in shape")
	}
	if r, _ := DecodeResult(ask(a, tb.ID())); r.Status != StatusAwaitingInitiatorSignature {
		t.Fatalf("the initiator got %v", r.Status)
	}
}

func TestFetchRefusesAnOlderCertificateSerial(t *testing.T) {
	s, a, b := standardPair(t)
	_, tb := startPairing(t, s, a, b)
	newer := partyFromSerial(t, seedTest1, seedTest2, 5)
	older := partyFromSerial(t, seedTest1, seedTest2, 3)
	ask := func(p Party) ResultStatus {
		frame := mustEncode(t, FetchMsg{ID: tb.ID()})
		sid := b.nextSID
		b.nextSID++
		next, _, _ := b.state.TargetOpen(b.world(), sid, s.now)
		b.state = next
		next, actions, err := b.state.TargetReceive(b.world(), Conn{Local: b.party, Remote: p, Protocol: simProtocol}, sid, frame, b.rng, s.now)
		must(t, err)
		b.state = next
		b.apply(actions, nil)
		r, err := DecodeResult(firstSend(t, actions))
		must(t, err)
		return r.Status
	}
	if got := ask(newer); got != StatusAwaitingInitiatorSignature {
		t.Fatalf("newer serial: %v", got)
	}
	if got := ask(older); got != StatusUnknown {
		t.Fatalf("older serial after a newer one: %v", got)
	}
	if got := ask(a.party); got != StatusUnknown { // serial 1
		t.Fatalf("the original serial: %v", got)
	}
	if r, _ := b.state.Target(tb.ID()); r.HighestSerial != 5 {
		t.Fatalf("highest serial %d", r.HighestSerial)
	}
}

func partyFromSerial(t testing.TB, identSeed, netSeed string, serial uint64) Party {
	return partyFrom(t, identSeed, netSeed, serial)
}

// A node retired or locally revoked after the owner confirmed is not signed
// for, at confirmation or at completion.
func TestPeerRevokedMeanwhileIsNotSignedFor(t *testing.T) {
	t.Run("at confirmation", func(t *testing.T) {
		s, a, b := standardPair(t)
		_, tb := startPairing(t, s, a, b)
		b.view.LocallyRevoked[a.party.node] = struct{}{}
		if out := b.confirm(tb.ID(), tb.Transcript.Code()); out.Result != ResultNotAccepted {
			t.Fatalf("outcome %+v", out)
		}
		rev, _ := revocation.Verify(mustRetire(t, seedTest1))
		_ = rev
	})
	t.Run("retired at confirmation", func(t *testing.T) {
		s, a, b := standardPair(t)
		_, tb := startPairing(t, s, a, b)
		rev, err := revocation.Verify(mustRetire(t, seedTest1))
		must(t, err)
		must(t, b.retired.Add(rev))
		if out := b.confirm(tb.ID(), tb.Transcript.Code()); out.Result != ResultNotAccepted {
			t.Fatalf("outcome %+v", out)
		}
	})
	t.Run("at completion on the target", func(t *testing.T) {
		s, a, b := standardPair(t)
		ia, tb := startPairing(t, s, a, b)
		b.confirm(tb.ID(), tb.Transcript.Code())
		b.view.LocallyRevoked[a.party.node] = struct{}{}
		a.confirm(ia.ID(), ia.Transcript.Code())
		s.run(2*time.Minute, 10*time.Second)
		if len(b.credentials) != 0 {
			t.Fatal("B signed for a node it had revoked")
		}
		if r, _ := b.state.Target(tb.ID()); r.Status != TargetRejected {
			t.Fatalf("B record %v", r.Status)
		}
		// A, whose signature left, is told and revokes.
		if r, _ := a.state.Initiator(ia.ID()); r.Status != InitiatorRejected || len(a.revocations) != 1 {
			t.Fatalf("A record %v, %d revocations", r.Status, len(a.revocations))
		}
	})
	t.Run("at completion on the initiator", func(t *testing.T) {
		s, a, b := standardPair(t)
		ia, tb := startPairing(t, s, a, b)
		a.confirm(ia.ID(), ia.Transcript.Code())
		s.run(10*time.Second, time.Second)
		a.view.LocallyRevoked[b.party.node] = struct{}{}
		b.confirm(tb.ID(), tb.Transcript.Code())
		s.run(2*time.Minute, 10*time.Second)
		if len(a.credentials) != 0 {
			t.Fatal("A stored a credential with a node it had revoked")
		}
		if r, _ := a.state.Initiator(ia.ID()); r.Status != InitiatorRejected || len(a.revocations) != 1 {
			t.Fatalf("A record %v, %d revocations", r.Status, len(a.revocations))
		}
	})
}

func TestAcceptedExpiryIsClampedByTheInitiator(t *testing.T) {
	s, a, b := standardPair(t)
	b.params.PendingTTL = 24 * time.Hour // a target that holds requests far longer than this node does
	ia, tb := startPairing(t, s, a, b)
	if tb.ExpiresAt.Sub(tb.ReceivedAt) != 24*time.Hour {
		t.Fatalf("target ttl %v", tb.ExpiresAt.Sub(tb.ReceivedAt))
	}
	want := ia.ReceivedAt.Add(a.params.PendingTTL + 2*time.Minute)
	if !ia.ExpiresAt.Equal(want) {
		t.Fatalf("initiator expiry %v, want %v", ia.ExpiresAt, want)
	}
}

func TestPendingRequestsSurviveRestarts(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.restart()
	a.restart()
	if r, ok := b.state.Target(tb.ID()); !ok || r.Transcript.Code() != tb.Transcript.Code() {
		t.Fatal("the target lost its request in a restart")
	}
	if r, ok := a.state.Initiator(ia.ID()); !ok || r.Transcript.Code() != ia.Transcript.Code() {
		t.Fatal("the initiator lost its request in a restart")
	}
	b.confirm(tb.ID(), tb.Transcript.Code())
	b.restart()
	a.confirm(ia.ID(), ia.Transcript.Code())
	a.restart()
	s.run(3*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
}

// Crashing between the storing of the credential and the marking of the
// request repairs itself and yields the same credential.
func TestRestartCompletesAConfirmedSignedRequest(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.confirm(tb.ID(), tb.Transcript.Code())
	a.confirm(ia.ID(), ia.Transcript.Code())
	// Deliver A's signature but crash B between StoreCredential and PersistTarget.
	s.tick()
	f := s.fetches[0]
	s.fetches = s.fetches[1:]
	tsid := b.nextSID
	b.nextSID++
	next, _, _ := b.state.TargetOpen(b.world(), tsid, s.now)
	b.state = next
	_, actions, err := b.state.TargetReceive(b.world(), b.conn(a), tsid, f.frame, b.rng, s.now)
	must(t, err)
	var first []Action
	for _, act := range actions {
		first = append(first, act)
		if _, ok := act.(StoreCredential); ok {
			break
		}
	}
	// The record with A's signature was never persisted either: B restarts from
	// the confirmed request alone.
	b.apply(first, nil)
	b.restart()
	if len(b.credentials) != 1 {
		t.Fatalf("credentials stored before the crash: %d", len(b.credentials))
	}
	var stored record.Record
	for _, r := range b.credentials {
		stored = r
	}
	// A asks again, B completes, and the credential is the same one.
	s.run(3*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
	var again record.Record
	for _, r := range b.credentials {
		again = r
	}
	e1, _ := stored.Encode()
	e2, _ := again.Encode()
	if !bytes.Equal(e1, e2) {
		t.Fatal("completing twice produced two different credentials")
	}
}

// Both decisions, the owner's confirmation and the initiator's signature, were
// in before the request expired, so a restart that comes only after the expiry
// still completes it: completing repeats what already happened, and the
// initiator, who is waiting for the credential, gets the same one.
func TestRestartAfterExpiryCompletesARequestDecidedBeforeIt(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.confirm(tb.ID(), tb.Transcript.Code())
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.tick()
	f := s.fetches[0]
	s.fetches = s.fetches[1:]
	tsid := b.nextSID
	b.nextSID++
	next, _, _ := b.state.TargetOpen(b.world(), tsid, s.now)
	b.state = next
	_, actions, err := b.state.TargetReceive(b.world(), b.conn(a), tsid, f.frame, b.rng, s.now)
	must(t, err)
	// B crashes right after storing the credential, before it marks the request.
	var upToTheStore []Action
	for _, act := range actions {
		upToTheStore = append(upToTheStore, act)
		if _, ok := act.(StoreCredential); ok {
			break
		}
	}
	b.apply(upToTheStore, nil)
	if len(b.credentials) != 1 {
		t.Fatalf("credentials stored before the crash: %d", len(b.credentials))
	}
	if r, _ := DecodeTargetRecord(b.disk["target/"+hexID(tb.ID())]); r.Status != TargetPending || r.Confirmation == nil || r.InitiatorSignature == nil {
		t.Fatalf("the record on disk is not pending, confirmed and signed: %+v", r)
	}
	// The node stays down until the request has expired.
	s.advance(tb.ExpiresAt.Sub(s.now) + time.Second)
	b.restart()
	if got, _ := b.state.Target(tb.ID()); got.Status != TargetComplete {
		t.Fatalf("after the restart the record is %v, want complete", got.Status)
	}
	if len(b.credentials) != 1 {
		t.Fatalf("credentials after the restart: %d", len(b.credentials))
	}
	s.run(3*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
}

// The window for deciding still closes at the expiry: a request that was
// confirmed, or signed, but not both by then ends as expired, restart or not.
func TestRestartAfterExpiryDoesNotCompleteAHalfDecidedRequest(t *testing.T) {
	for name, decide := range map[string]func(rec *TargetRecord, ia InitiatorRecord, a *simNode){
		"only the confirmation": func(rec *TargetRecord, _ InitiatorRecord, _ *simNode) {
			conf := Confirmation{Method: ConfirmedByCode, By: LocalAuthor(), Operation: admin.OperationID{1}, At: rec.ReceivedAt}
			rec.Confirmation = &conf
		},
		"only the signature": func(rec *TargetRecord, ia InitiatorRecord, a *simNode) {
			sig := mustSeal(t, ia.Transcript.Credential(), a.signer)
			rec.InitiatorSignature = &sig
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, a, b := standardPair(t)
			ia, tb := startPairing(t, s, a, b)
			rec := tb
			decide(&rec, ia, a)
			s.advance(tb.ExpiresAt.Sub(s.now) + time.Second)
			st, err := Restore(Snapshot{Targets: []TargetRecord{rec}})
			must(t, err)
			next, actions, err := st.Recover(b.world(), s.now)
			must(t, err)
			for _, act := range actions {
				if _, stored := act.(StoreCredential); stored {
					t.Fatal("a credential was stored for a request that was not fully decided")
				}
			}
			if got, _ := next.Target(tb.ID()); got.Status != TargetPending {
				t.Fatalf("record %v", got.Status)
			}
			next, _, err = next.Tick(b.world(), b.rng, s.now)
			must(t, err)
			if got, _ := next.Target(tb.ID()); got.Status != TargetExpired {
				t.Fatalf("after a tick the record is %v, want expired", got.Status)
			}
		})
	}
}

func TestManagementAuthorization(t *testing.T) {
	s, a, b := standardPair(t)
	_, tb := startPairing(t, s, a, b)
	_, viewer := b.grantPerson(1, admin.LevelView)
	_, manager := b.grantPerson(2, admin.LevelManage)
	_, boss := b.grantPerson(3, admin.LevelFullControl)
	code := tb.Transcript.Code()

	confirmAs := func(actor admin.Actor) Outcome {
		req := b.as(actor)
		next, out, actions, err := b.state.Confirm(b.world(), req, tb.ID(), code, s.now)
		must(t, err)
		if out.Disposition == DispositionExecuted && out.Result == ResultOK {
			b.state = next
			b.apply(actions, nil)
		}
		return out
	}
	t.Run("a viewer cannot confirm", func(t *testing.T) {
		if out := confirmAs(viewer); out.Disposition != DispositionRefused || out.Reason != admin.ReasonListLevelTooLow {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("a manager cannot confirm", func(t *testing.T) {
		if out := confirmAs(manager); out.Disposition != DispositionRefused || out.Reason != admin.ReasonListLevelTooLow {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("a manager cannot enter an expectation but can reject and forget", func(t *testing.T) {
		req := b.as(manager)
		_, out, _, err := b.state.Expect(b.world(), req, a.party.node, s.now)
		must(t, err)
		if out.Disposition != DispositionRefused {
			t.Fatalf("manager entered an expectation: %+v", out)
		}
		req = b.as(boss)
		next, out, actions, err := b.state.Expect(b.world(), req, a.party.node, s.now)
		must(t, err)
		if out.Result != ResultOK {
			t.Fatal(out)
		}
		b.state = next
		b.apply(actions, nil)
		req = b.as(manager)
		next, out, actions, err = b.state.Forget(b.world(), req, a.party.node, s.now)
		must(t, err)
		if out.Disposition != DispositionExecuted || out.Result != ResultOK {
			t.Fatalf("manager forgetting: %+v", out)
		}
		b.state = next
		b.apply(actions, nil)
		if len(b.state.Expectations()) != 0 {
			t.Fatal("forgotten expectation remains")
		}
		req = b.as(viewer)
		_, out, _, _ = b.state.Forget(b.world(), req, a.party.node, s.now)
		if out.Disposition != DispositionRefused {
			t.Fatalf("viewer forgot: %+v", out)
		}
	})
	t.Run("a remote person with full control confirms", func(t *testing.T) {
		out := confirmAs(boss)
		if out.Disposition != DispositionExecuted || out.Result != ResultOK {
			t.Fatalf("%+v", out)
		}
		rec, _ := b.state.Target(tb.ID())
		if rec.Confirmation == nil || rec.Confirmation.By.IsLocalOwner() || rec.Confirmation.Method != ConfirmedByCode {
			t.Fatalf("confirmation %+v", rec.Confirmation)
		}
	})
	t.Run("a device cap below full control is not enough", func(t *testing.T) {
		_, capped := b.grantPerson(4, admin.LevelFullControl)
		capped.Cert.MaxLevel = admin.LevelManage
		req := b.as(capped)
		_, out, _, _ := b.state.Confirm(b.world(), req, tb.ID(), code, s.now)
		if out.Disposition != DispositionRefused || out.Reason != admin.ReasonDeviceCapTooLow {
			t.Fatalf("%+v", out)
		}
	})
}

func TestOperationIdentifierSemantics(t *testing.T) {
	s, a, b := standardPair(t)
	_, tb := startPairing(t, s, a, b)
	code := tb.Transcript.Code()
	req := b.owner()
	// A missing operation identifier is invalid.
	zero := Request{Actor: admin.LocalOwner()}
	_, out, _, _ := b.state.Confirm(b.world(), zero, tb.ID(), code, s.now)
	if out.Disposition != DispositionRefused || out.Reason != admin.ReasonMissingOperationID {
		t.Fatalf("%+v", out)
	}
	wrong := (code + 1) % 1000000
	next, out, _, err := b.state.Confirm(b.world(), req, tb.ID(), wrong, s.now)
	must(t, err)
	b.state = next
	b.remember(out, req)
	if out.Result != ResultWrongCode {
		t.Fatalf("%+v", out)
	}
	// The same request again is a replay, not a second attempt.
	_, again, actions, _ := b.state.Confirm(b.world(), req, tb.ID(), wrong, s.now)
	if again.Disposition != DispositionReplayed || len(actions) != 0 {
		t.Fatalf("%+v", again)
	}
	// The same identifier with another code, or another pairing, is a conflict.
	_, conflict, _, _ := b.state.Confirm(b.world(), req, tb.ID(), code, s.now)
	if conflict.Disposition != DispositionRefused || conflict.Reason != admin.ReasonOperationIDReused {
		t.Fatalf("%+v", conflict)
	}
	_, conflict, _, _ = b.state.Reject(b.world(), req, tb.ID(), WithdrawDeclined, s.now)
	if conflict.Disposition != DispositionRefused || conflict.Reason != admin.ReasonOperationIDReused {
		t.Fatalf("a different operation under the same identifier: %+v", conflict)
	}
	// Results are filed per person: another person's identifier is another request.
	_, boss := b.grantPerson(3, admin.LevelFullControl)
	reqBoss := b.as(boss)
	reqBoss.ID = req.ID
	_, fresh, _, _ := b.state.Confirm(b.world(), reqBoss, tb.ID(), wrong, s.now)
	if fresh.Disposition != DispositionExecuted {
		t.Fatalf("%+v", fresh)
	}
}

func TestExpectationRules(t *testing.T) {
	t.Run("time to live", func(t *testing.T) {
		s, a, b := standardPair(t)
		b.expect(a)
		s.advance(59*time.Minute + 59*time.Second)
		if _, ok := b.state.liveExpectation(b.world(), a.party.node, s.now); !ok {
			t.Fatal("expired early")
		}
		s.advance(time.Second)
		if _, ok := b.state.liveExpectation(b.world(), a.party.node, s.now); ok {
			t.Fatal("still live at the time to live")
		}
		_, tb := startPairing(t, s, a, b)
		if !tb.Expected.IsZero() || tb.Confirmation != nil {
			t.Fatal("an expired expectation pre-confirmed a request")
		}
		s.tick()
		if len(b.state.Expectations()) != 0 {
			t.Fatal("the expired expectation is kept")
		}
	})
	t.Run("single use", func(t *testing.T) {
		s, a, b := standardPair(t)
		a.expect(b)
		b.expect(a)
		_, tb := startPairing(t, s, a, b)
		if tb.Confirmation == nil {
			t.Fatal("not pre-confirmed")
		}
		if len(b.state.Expectations()) != 0 || len(a.state.Expectations()) != 0 {
			t.Fatal("an expectation survived its use")
		}
	})
	t.Run("capacity", func(t *testing.T) {
		s, _, b := standardPair(t)
		var nodes []*simNode
		for i := 0; i < 33; i++ {
			nodes = append(nodes, s.addGenerated("C", 50+i))
		}
		for i := 0; i < 32; i++ {
			if out := b.expect(nodes[i]); out.Result != ResultOK {
				t.Fatalf("expectation %d: %+v", i, out)
			}
		}
		if out := b.expect(nodes[32]); out.Result != ResultFull {
			t.Fatalf("33rd: %+v", out)
		}
		// Entering the same node again replaces and does not need room.
		if out := b.expect(nodes[0]); out.Result != ResultOK {
			t.Fatalf("re-entering: %+v", out)
		}
		if len(b.state.Expectations()) != 32 {
			t.Fatalf("%d expectations", len(b.state.Expectations()))
		}
	})
	t.Run("not for itself", func(t *testing.T) {
		_, _, b := standardPair(t)
		if out := b.expect(b); out.Result != ResultInvalid {
			t.Fatalf("%+v", out)
		}
	})
	t.Run("an author who loses full control invalidates it", func(t *testing.T) {
		s, a, b := standardPair(t)
		author, boss := b.grantPerson(3, admin.LevelFullControl)
		req := b.as(boss)
		next, out, actions, err := b.state.Expect(b.world(), req, a.party.node, s.now)
		must(t, err)
		if out.Result != ResultOK {
			t.Fatal(out)
		}
		b.state = next
		b.apply(actions, nil)
		if e := b.state.Expectations()[0]; e.Author != author {
			t.Fatalf("author %+v", e.Author)
		}
		// The person is demoted to manage.
		entries := b.list.Entries()
		entries[0].Level = admin.LevelManage
		b.list, _ = admin.NewList(entries)
		if _, ok := b.state.liveExpectation(b.world(), a.party.node, s.now); ok {
			t.Fatal("the expectation outlived its author's authority")
		}
		_, tb := startPairing(t, s, a, b)
		if !tb.Expected.IsZero() {
			t.Fatal("pre-confirmed by a demoted author")
		}
		s.tick()
		if len(b.state.Expectations()) != 0 {
			t.Fatal("the invalid expectation is kept")
		}
	})
	t.Run("a removed person invalidates it", func(t *testing.T) {
		s, a, b := standardPair(t)
		_, boss := b.grantPerson(3, admin.LevelFullControl)
		req := b.as(boss)
		next, _, _, _ := b.state.Expect(b.world(), req, a.party.node, s.now)
		b.state = next
		b.list, _ = admin.NewList(nil)
		if _, ok := b.state.liveExpectation(b.world(), a.party.node, s.now); ok {
			t.Fatal("expectation of a removed person is live")
		}
	})
}

func TestBothSidesExpectingNeedNoHuman(t *testing.T) {
	s, a, b := standardPair(t)
	a.expect(b)
	b.expect(a)
	ia, tb := startPairing(t, s, a, b)
	if ia.Confirmation == nil || ia.Confirmation.Method != ConfirmedByExpectation || tb.Confirmation == nil || tb.Confirmation.Method != ConfirmedByExpectation {
		t.Fatal("not pre-confirmed on both sides")
	}
	s.run(3*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
}

// The code is shown on a side that confirmed through an expectation as long as
// the request is pending, for the other side to compare.
func TestExpectingSideStillShowsTheCode(t *testing.T) {
	s, a, b := standardPair(t)
	b.expect(a)
	ia, tb := startPairing(t, s, a, b)
	if tb.Status != TargetPending || tb.Display().Code != ia.Transcript.Code() {
		t.Fatal("the pre-confirmed side does not show the code")
	}
	// A did not expect B: it must confirm, and it cannot skip by naming B.
	if ia.Confirmation != nil || ia.Signature != nil {
		t.Fatal("A confirmed without its owner")
	}
	s.run(3*time.Minute, 10*time.Second)
	if len(a.credentials)+len(b.credentials) != 0 {
		t.Fatal("a credential without A's owner")
	}
	if out := a.confirm(ia.ID(), ia.Transcript.Code()); out.Result != ResultOK {
		t.Fatal(out)
	}
	s.run(3*time.Minute, 10*time.Second)
	assertCompleted(t, a, b, ia.ID())
}

func TestKnowingTheOthersIDDoesNotSkipVerification(t *testing.T) {
	s, a, b := standardPair(t)
	// A names B's node identifier when initiating, B names nothing.
	ia, tb := startPairing(t, s, a, b)
	if ia.Confirmation != nil || tb.Confirmation != nil || !ia.Expected.IsZero() {
		t.Fatal("naming the target is not an expectation")
	}
	s.run(5*time.Minute, 10*time.Second)
	if len(a.credentials)+len(b.credentials) != 0 {
		t.Fatal("completed without verification")
	}
	// Likewise B expecting A while A is a different node than expected.
	s2, a2, b2 := standardPair(t)
	other := s2.addGenerated("C", 7)
	b2.expect(other)
	_, tb2 := startPairing(t, s2, a2, b2)
	if tb2.Confirmation != nil {
		t.Fatal("an expectation for another node skipped verification")
	}
}

// Two requests awaiting their owners that show the same code must not be
// confirmed by code: the owner cannot tell which one the code belongs to.
func TestEqualCodesRefuseConfirmation(t *testing.T) {
	s, _, b := standardPair(t)
	// Find two transcripts with the same code by a birthday search.
	byCode := map[Code]TargetRecord{}
	var first, second TargetRecord
	rng := detReader{rand.New(rand.NewSource(11))}
	initiators := []*simNode{s.addGenerated("C", 70), s.addGenerated("C", 71)}
	for i := 0; second.Transcript.IsZero(); i++ {
		c := initiators[i%2]
		terms := Terms{Protocol: simProtocol, Initiator: c.party, Target: b.party, CreatedAt: wholeNow(s.now)}
		nA, _ := NewNonce(rng)
		nB, _ := NewNonce(rng)
		commit, _ := terms.Commit(nA)
		tr, err := Open(terms, commit, nB, nA)
		must(t, err)
		rec := TargetRecord{Transcript: tr, ReceivedAt: wholeNow(s.now), ExpiresAt: wholeNow(s.now).Add(time.Hour), HighestSerial: c.party.serial, Status: TargetPending}
		if prev, ok := byCode[tr.Code()]; ok && prev.Transcript.terms.Initiator.node != c.party.node {
			first, second = prev, rec
			break
		}
		if _, ok := byCode[tr.Code()]; !ok {
			byCode[tr.Code()] = rec
		}
		if i > 200000 {
			t.Fatal("no collision found")
		}
	}
	st, err := Restore(Snapshot{Targets: []TargetRecord{first, second}})
	must(t, err)
	b.state = st
	for _, rec := range []TargetRecord{first, second} {
		out := b.confirm(rec.ID(), rec.Transcript.Code())
		if out.Result != ResultAmbiguousCode {
			t.Fatalf("outcome %+v", out)
		}
	}
	// Rejecting one of them removes the ambiguity.
	b.reject(second.ID(), WithdrawDeclined)
	if out := b.confirm(first.ID(), first.Transcript.Code()); out.Result != ResultOK {
		t.Fatalf("after the other was rejected: %+v", out)
	}
}

func lastNotice(n *simNode) Notice {
	if len(n.notices) == 0 {
		return Notice{}
	}
	return n.notices[len(n.notices)-1]
}

func TestInitiatorReportsAnUsedAttemptAfterOpeningItsNonce(t *testing.T) {
	t.Run("the acceptance never comes", func(t *testing.T) {
		s, a, b := standardPair(t)
		s.initiate(a, b, Initiation{})
		s.deliverOne(0, false) // M1
		s.deliverOne(0, false) // M2
		s.deliverOne(0, false) // M3: B saves the request and accepts
		for len(s.queue) > 0 {
			s.deliverOne(0, true) // M4 and the close are lost
		}
		if _, ok := b.pendingFrom(a); !ok {
			t.Fatal("B holds no orphan")
		}
		s.advance(31 * time.Second)
		s.tick()
		if n := lastNotice(a); n.Kind != NoticeAttemptUsed {
			t.Fatalf("notice %+v", n)
		}
		if len(a.state.Initiators()) != 0 {
			t.Fatal("A saved a request without the acceptance")
		}
	})
	t.Run("the target closes after the open", func(t *testing.T) {
		s, a, b := standardPair(t)
		st, _ := s.initiate(a, b, Initiation{})
		s.deliverOne(0, false)
		s.deliverOne(0, false)
		// B is gone before M3 arrives.
		b.restart()
		s.deliverOne(0, false)
		s.pump()
		_ = st
		if n := lastNotice(a); n.Kind != NoticeAttemptUsed {
			t.Fatalf("notice %+v", n)
		}
	})
	t.Run("an acceptance for another request", func(t *testing.T) {
		s, a, b := standardPair(t)
		s.initiate(a, b, Initiation{})
		s.deliverOne(0, false)
		s.deliverOne(0, false)
		s.deliverOne(0, true) // M3 lost; replace the acceptance with a forged one
		s.queue = nil
		sess := a.state.sessions[0]
		forged := mustEncode(t, AcceptedMsg{ID: ID{1, 2, 3}, ExpiresAt: s.now.Add(time.Hour)})
		next, _, err := a.state.InitiatorReceive(a.world(), sess.id, forged, s.now)
		must(t, err)
		a.state = next
		if len(a.state.Initiators()) != 0 {
			t.Fatal("A accepted a pairing identifier it did not compute")
		}
	})
	t.Run("a refusal before the open uses no attempt", func(t *testing.T) {
		s, a, b := standardPair(t)
		b.view.LocallyRevoked[a.party.node] = struct{}{}
		s.initiate(a, b, Initiation{})
		s.pump()
		if n := lastNotice(a); n.Kind != NoticeNotStarted || n.Refusal != RefusalNotAccepted {
			t.Fatalf("notice %+v", n)
		}
	})
}

func TestInitiatorNeverReruns(t *testing.T) {
	s, a, b := standardPair(t)
	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false)
	for len(s.queue) > 0 {
		s.deliverOne(0, true)
	}
	before := len(s.events)
	s.run(3*time.Hour, time.Minute)
	for _, e := range s.events[before:] {
		if e.Kind == "send" && e.Node == "A" {
			t.Fatal("A sent a frame on its own after the segment ended")
		}
	}
	if len(a.state.sessions) != 0 || len(a.state.Initiators()) != 0 {
		t.Fatal("A holds state of a segment it gave up")
	}
}

func TestPersistedRecordsRebuildTheirDerivedValues(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(10*time.Second, time.Second)
	for name, bytes := range map[string][]byte{"target": b.disk["target/"+hexID(tb.ID())], "initiator": a.disk["initiator/"+hexID(ia.ID())]} {
		if bytes == nil {
			t.Fatalf("%s record not persisted", name)
		}
	}
	// A client given the target record rebuilds the display from the raw fields.
	rec, err := DecodeTargetRecord(b.disk["target/"+hexID(tb.ID())])
	must(t, err)
	id, code := oracle(t, rec.Transcript)
	d := rec.Display()
	if d.ID != id || d.Code != code || d.Local != b.party.node || d.Remote != a.party.node || d.Initiating {
		t.Fatalf("display %+v differs from the oracle (%x, %v)", d, id, code)
	}
	if got := d.Code.String(); len(got) != 7 || got[3] != ' ' {
		t.Fatalf("code text %q", got)
	}
	ri, err := DecodeInitiatorRecord(a.disk["initiator/"+hexID(ia.ID())])
	must(t, err)
	if di := ri.Display(); di.Code != code || !di.Initiating || di.Local != a.party.node {
		t.Fatalf("initiator display %+v", di)
	}
}

func TestDecodingRefusesTamperedPersistedRecords(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	good := b.disk["target/"+hexID(tb.ID())]
	if _, err := DecodeTargetRecord(good); err != nil {
		t.Fatal(err)
	}
	t.Run("a flipped nonce byte fails the commitment", func(t *testing.T) {
		nB, _ := tb.Transcript.Nonces()
		bad := bytes.Replace(good, nB[:], append([]byte{nB[0] ^ 1}, nB[1:]...), 1)
		// nB is revealed after the commitment, so changing it still opens; the
		// pairing identifier changes instead: the record is another request.
		rec, err := DecodeTargetRecord(bad)
		if err == nil && rec.ID() == tb.ID() {
			t.Fatal("tampering did not change the identifier")
		}
	})
	t.Run("a flipped nA byte fails the commitment", func(t *testing.T) {
		_, nA := tb.Transcript.Nonces()
		bad := bytes.Replace(good, nA[:], append([]byte{nA[0] ^ 1}, nA[1:]...), 1)
		if _, err := DecodeTargetRecord(bad); err == nil {
			t.Fatal("decoded")
		}
	})
	t.Run("a record is not an initiator record", func(t *testing.T) {
		if _, err := DecodeInitiatorRecord(good); err == nil {
			t.Fatal("decoded")
		}
		if _, err := DecodeTargetRecord(a.disk["initiator/"+hexID(ia.ID())]); err == nil {
			t.Fatal("decoded")
		}
	})
	t.Run("trailing and truncated bytes", func(t *testing.T) {
		if _, err := DecodeTargetRecord(append(append([]byte{}, good...), 0)); err == nil {
			t.Fatal("decoded trailing byte")
		}
		if _, err := DecodeTargetRecord(good[:len(good)-1]); err == nil {
			t.Fatal("decoded truncated")
		}
	})
	t.Run("restore refuses repeated identifiers and invalid records", func(t *testing.T) {
		if _, err := Restore(Snapshot{Targets: []TargetRecord{tb, tb}}); err == nil {
			t.Fatal("duplicate accepted")
		}
		bad := tb
		bad.Status = TargetComplete
		if _, err := Restore(Snapshot{Targets: []TargetRecord{bad}}); err == nil {
			t.Fatal("a complete record without a credential accepted")
		}
		bad = tb
		bad.Rejection = WithdrawDeclined
		if _, err := Restore(Snapshot{Targets: []TargetRecord{bad}}); err == nil {
			t.Fatal("a pending record with a rejection accepted")
		}
	})
}

// Records and counters round trip.
func FuzzDecodeTargetRecord(f *testing.F) {
	seedPersisted(f, func(s *sim, a, b *simNode) [][]byte {
		var out [][]byte
		for k, v := range b.disk {
			if len(k) > 7 && k[:7] == "target/" {
				out = append(out, v)
			}
		}
		return out
	})
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := DecodeTargetRecord(data)
		if err != nil {
			return
		}
		again, err := r.Encode()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func FuzzDecodeInitiatorRecord(f *testing.F) {
	seedPersisted(f, func(s *sim, a, b *simNode) [][]byte {
		var out [][]byte
		for k, v := range a.disk {
			if len(k) > 10 && k[:10] == "initiator/" {
				out = append(out, v)
			}
		}
		return out
	})
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := DecodeInitiatorRecord(data)
		if err != nil {
			return
		}
		again, err := r.Encode()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func FuzzDecodeLimits(f *testing.F) {
	l := Limits{}
	l, _ = l.SpendReveal(DefaultParams(), testNow)
	l = l.RecordFailure(DefaultParams(), testNow)
	b, _ := l.Encode()
	f.Add(b)
	empty, _ := Limits{}.Encode()
	f.Add(empty)
	f.Fuzz(func(t *testing.T, data []byte) {
		l, err := DecodeLimits(data)
		if err != nil {
			return
		}
		again, err := l.Encode()
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

func FuzzDecodeExpectations(f *testing.F) {
	s, a, b := standardPair(f)
	_ = s
	b.expect(a)
	f.Add(b.disk["expectations"])
	empty, _ := EncodeExpectations(nil)
	f.Add(empty)
	f.Fuzz(func(t *testing.T, data []byte) {
		l, err := DecodeExpectations(data)
		if err != nil {
			return
		}
		again, err := EncodeExpectations(l)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("round trip: %v", err)
		}
	})
}

// An expectation is asked again when the request is saved, not only when the
// segment starts: an author demoted in between does not pre-confirm anything.
func TestAuthorDemotedBetweenRevealAndOpen(t *testing.T) {
	s, a, b := standardPair(t)
	_, boss := b.grantPerson(3, admin.LevelFullControl)
	req := b.as(boss)
	next, out, actions, err := b.state.Expect(b.world(), req, a.party.node, s.now)
	must(t, err)
	if out.Result != ResultOK {
		t.Fatal(out)
	}
	b.state = next
	b.apply(actions, nil)

	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false) // M1: B reveals as it would to an expected node
	entries := b.list.Entries()
	entries[0].Level = admin.LevelManage
	b.list, _ = admin.NewList(entries)
	s.pump()
	if len(b.state.Targets()) != 0 {
		t.Fatal("a request was saved on the strength of an expectation that no longer stood")
	}
	if n := lastNotice(a); n.Kind != NoticeAttemptUsed {
		t.Fatalf("notice %+v", n)
	}
}

// A newer segment from the same initiator replaces its older one that is still
// open, so a retry never holds two reservations.
func TestNewerSegmentFromTheSameInitiatorReplacesTheOlder(t *testing.T) {
	s, a, b := standardPair(t)
	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false) // M1
	for len(s.queue) > 0 {
		s.deliverOne(0, true)
	}
	if got := b.state.unexpectedWaiting(s.now, ident.NodeID{}); got != 1 {
		t.Fatalf("reservations %d", got)
	}
	a.restart() // A's segment is gone; B's lingers
	s.queue = nil
	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false)
	if got := b.state.unexpectedWaiting(s.now, ident.NodeID{}); got != 1 {
		t.Fatalf("reservations after the retry: %d, want 1", got)
	}
	if len(b.state.sessions) != 1 {
		t.Fatalf("B keeps %d sessions", len(b.state.sessions))
	}
}

// fetchAs sends a fetch frame to the target as the given party and returns the
// status it answers.
func fetchAs(t testing.TB, s *sim, b *simNode, from Party, f FetchMsg) ResultMsg {
	t.Helper()
	sid := b.nextSID
	b.nextSID++
	next, _, err := b.state.TargetOpen(b.world(), sid, s.now)
	must(t, err)
	b.state = next
	next, actions, err := b.state.TargetReceive(b.world(), Conn{Local: b.party, Remote: from, Protocol: simProtocol}, sid, mustEncode(t, f), b.rng, s.now)
	must(t, err)
	b.state = next
	b.apply(actions, nil)
	r, err := DecodeResult(firstSend(t, actions))
	must(t, err)
	return r
}

// A signature that is not the initiator's, or is over another credential, is
// not kept and does not complete anything.
func TestTargetIgnoresAnInitiatorSignatureThatIsNotTheInitiators(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.confirm(tb.ID(), tb.Transcript.Code())
	cred := ia.Transcript.Credential()

	byTarget, err := record.Seal(credential.Descriptor, cred, b.signer) // right payload, the target's own key
	must(t, err)
	other := credential.New(a.party.node, b.party.node, ia.Transcript.Terms().CreatedAt, time.Time{}, credential.Nonce{9})
	otherPayload, err := record.Seal(credential.Descriptor, other, a.signer) // the initiator's key, another payload
	must(t, err)
	both, err := record.CoSign(credential.Descriptor, mustSeal(t, cred, a.signer), b.signer) // a complete record
	must(t, err)
	for name, rec := range map[string]record.Record{"signed by the target": byTarget, "another payload": otherPayload, "already complete": both} {
		rec := rec
		r := fetchAs(t, s, b, a.party, FetchMsg{ID: tb.ID(), Signed: &rec})
		if r.Status != StatusAwaitingInitiatorSignature {
			t.Fatalf("%s: answered %v", name, r.Status)
		}
		if got, _ := b.state.Target(tb.ID()); got.InitiatorSignature != nil || got.Status != TargetPending {
			t.Fatalf("%s: the record changed", name)
		}
	}
	if len(b.credentials) != 0 {
		t.Fatal("a credential was made from a signature that is not the initiator's")
	}
}

func mustSeal(t testing.TB, c credential.Credential, signer record.Signer) record.Record {
	t.Helper()
	r, err := record.Seal(credential.Descriptor, c, signer)
	must(t, err)
	return r
}

// A target does not complete after its request expired, even when it was
// confirmed and the initiator's signature arrives late.
func TestLateSignatureDoesNotCompleteAnExpiredRequest(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.confirm(tb.ID(), tb.Transcript.Code())
	sig := mustSeal(t, ia.Transcript.Credential(), a.signer)
	s.advance(time.Hour) // no tick: the expiry is applied by the fetch itself
	r := fetchAs(t, s, b, a.party, FetchMsg{ID: tb.ID(), Signed: &sig})
	if r.Status != StatusExpired {
		t.Fatalf("answered %v", r.Status)
	}
	if len(b.credentials) != 0 {
		t.Fatal("completed after expiry")
	}
}

func TestInitiatorEndsAtTheTimeLimitOnAReveal(t *testing.T) {
	s, a, b := standardPair(t)
	s.initiate(a, b, Initiation{})
	s.deliverOne(0, false) // M1: B's reveal is queued
	s.advance(31 * time.Second)
	for len(s.queue) > 0 {
		s.deliverOne(0, false)
	}
	for _, e := range s.events {
		if e.Node == "A" && e.Kind == "send" {
			if _, err := DecodeOpen(e.Frame); err == nil && e.Frame != nil {
				// The only open sent is the one of the first M1/M2 round, which never happened here.
				t.Fatal("A opened its nonce after its own time limit")
			}
		}
	}
	if n := lastNotice(a); n.Kind != NoticeNotStarted {
		t.Fatalf("notice %+v", n)
	}
}

func TestInitiatorIgnoresAnAnswerFromAnotherNode(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	other := s.addGenerated("C", 5)
	// A forged "unknown" from a node that is not the target must not end anything.
	next, _, err := a.state.OnFetchResult(a.world(), ia.ID(), other.party, mustEncode(t, ResultMsg{ID: ia.ID(), Status: StatusUnknown}), a.rng, s.now)
	must(t, err)
	if r, _ := next.Initiator(ia.ID()); r.Status != InitiatorActive {
		t.Fatalf("A ended its request on the word of another node: %v", r.Status)
	}
	next, _, err = a.state.OnFetchResult(a.world(), ia.ID(), b.party, mustEncode(t, ResultMsg{ID: ia.ID(), Status: StatusUnknown}), a.rng, s.now)
	must(t, err)
	if r, _ := next.Initiator(ia.ID()); r.Status != InitiatorUnknown {
		t.Fatalf("the target's own answer was ignored: %v", r.Status)
	}
	_ = tb
}

func TestInitiatorGivesUpAndRevokesWhenTheTargetNeverAnswers(t *testing.T) {
	s, a, b := standardPair(t)
	ia, _ := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(30*time.Second, 5*time.Second)
	s.dropFetch = func(simFetch) bool { return true }
	s.run(24*time.Hour+30*time.Minute, 10*time.Minute) // up to the target's expiry plus retention is not yet over
	if r, _ := a.state.Initiator(ia.ID()); r.Status != InitiatorActive {
		t.Fatalf("A gave up early: %v", r.Status)
	}
	if len(a.revocations) != 0 {
		t.Fatal("revoked while the answer could still come")
	}
	s.run(2*time.Hour, 10*time.Minute)
	r, _ := a.state.Initiator(ia.ID())
	if r.Status != InitiatorGaveUp {
		t.Fatalf("A record %v", r.Status)
	}
	if len(a.revocations) != 1 {
		t.Fatalf("revocations %d", len(a.revocations))
	}
}

func TestFetchBackoff(t *testing.T) {
	p := DefaultParams()
	rng := detReader{rand.New(rand.NewSource(3))}
	prev := time.Duration(0)
	for n := 1; n <= 12; n++ {
		want := p.FetchBackoffMin << (n - 1)
		if want > p.FetchBackoffMax {
			want = p.FetchBackoffMax
		}
		low, high := time.Hour, time.Duration(0)
		for i := 0; i < 200; i++ {
			d := backoff(p, n, rng)
			low, high = min(low, d), max(high, d)
			if d > want || d < max(want/2, p.FetchBackoffMin) {
				t.Fatalf("attempt %d: %v outside [%v, %v]", n, d, max(want/2, p.FetchBackoffMin), want)
			}
		}
		if n >= 3 && high-low < want/4 {
			t.Fatalf("attempt %d: no jitter (%v to %v)", n, low, high)
		}
		if want < prev {
			t.Fatal("not monotone")
		}
		prev = want
	}
	if got := backoff(p, 1, rng); got != time.Second {
		t.Fatalf("first delay %v", got)
	}
	if got := backoff(p, 40, bytes.NewReader(nil)); got != p.FetchBackoffMax {
		t.Fatalf("a failing source of randomness: %v, want the unjittered delay", got)
	}
}

func TestPauseStopsConfirmingAnUnexpectedRequest(t *testing.T) {
	s, a, b := standardPair(t)
	_, tb := startPairing(t, s, a, b)
	for i := 0; i < 3; i++ {
		b.state = b.state.withLimits(b.state.limits.RecordFailure(b.params, s.now.Add(-30*time.Minute)))
	}
	out := b.confirm(tb.ID(), tb.Transcript.Code())
	if out.Result != ResultPaused || out.RetryAfter != 30*time.Minute {
		t.Fatalf("outcome %+v", out)
	}
	if r, _ := b.state.Target(tb.ID()); r.Confirmation != nil {
		t.Fatal("confirmed during the pause")
	}
	// The request is kept; after the pause it can be confirmed.
	s.advance(30 * time.Minute)
	if out := b.confirm(tb.ID(), tb.Transcript.Code()); out.Result != ResultOK {
		t.Fatalf("after the pause: %+v", out)
	}
}

func TestInitiateNamesTheNodeItReaches(t *testing.T) {
	s, a, b := standardPair(t)
	c := s.addGenerated("C", 4)
	_, out := s.initiate(a, b, Initiation{Target: c.party.node})
	if out.Disposition != DispositionExecuted || out.Result != ResultWrongPeer {
		t.Fatalf("outcome %+v", out)
	}
	if len(s.queue) != 0 {
		t.Fatal("a frame was sent to a node that is not the one named")
	}
}

// Recovery repeats what a crash may have left undone, from records alone.
func TestRecoverRepeatsTheRevocationOfAReleasedSignature(t *testing.T) {
	s, a, b := standardPair(t)
	ia, _ := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(10*time.Second, time.Second)
	a.reject(ia.ID(), WithdrawDeclined)
	// The revocation store lost what it had.
	a.revocations = map[record.Digest]record.Record{}
	a.restart()
	if len(a.revocations) != 1 {
		t.Fatalf("revocations after recovery: %d", len(a.revocations))
	}
}

func TestRecoverCompletesAConfirmedRequestThatHoldsTheSignature(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	rec := tb
	conf := Confirmation{Method: ConfirmedByCode, By: LocalAuthor(), Operation: admin.OperationID{1}, At: wholeNow(s.now)}
	sig := mustSeal(t, ia.Transcript.Credential(), a.signer)
	rec.Confirmation, rec.InitiatorSignature = &conf, &sig
	st, err := Restore(Snapshot{Targets: []TargetRecord{rec}})
	must(t, err)
	b.state = st
	next, actions, err := b.state.Recover(b.world(), s.now)
	must(t, err)
	b.state = next
	b.apply(actions, nil)
	if len(b.credentials) != 1 {
		t.Fatal("recovery did not complete the request")
	}
	if got, _ := b.state.Target(tb.ID()); got.Status != TargetComplete {
		t.Fatalf("record %v", got.Status)
	}
}

// A confirmation by the local owner is stored under the persisted author kind
// of the local owner, in a target record as in an initiator record.
func TestStoredRecordsCarryThePersistedAuthorKind(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	b.confirm(tb.ID(), tb.Transcript.Code())
	a.confirm(ia.ID(), ia.Transcript.Code())
	var target envelope[targetWire]
	must(t, record.UnmarshalStrict(b.disk["target/"+hexID(tb.ID())], &target, MaxPersisted))
	var initiator envelope[initiatorWire]
	must(t, record.UnmarshalStrict(a.disk["initiator/"+hexID(ia.ID())], &initiator, MaxPersisted))
	if c := target.Payload.Confirmation; c == nil || c.By.Kind != authorKindLocalOwner || authorKindLocalOwner != 1 {
		t.Fatalf("target confirmation %+v", c)
	}
	if c := initiator.Payload.Confirmation; c == nil || c.By.Kind != 1 {
		t.Fatalf("initiator confirmation %+v", c)
	}
}

func TestDecodingRefusesAnInconsistentRecord(t *testing.T) {
	s, a, b := standardPair(t)
	_, tb := startPairing(t, s, a, b)
	var stored envelope[targetWire]
	must(t, record.UnmarshalStrict(b.disk["target/"+hexID(tb.ID())], &stored, MaxPersisted))
	for name, mutate := range map[string]func(*targetWire){
		"complete without a credential":  func(w *targetWire) { w.Status = uint64(TargetComplete) },
		"rejected without a reason":      func(w *targetWire) { w.Status = uint64(TargetRejected); e := int64(5); w.EndedAt = &e },
		"a status outside the table":     func(w *targetWire) { w.Status = 9 },
		"expires before it was received": func(w *targetWire) { w.ExpiresAt = w.ReceivedAt },
	} {
		bad := stored
		mutate(&bad.Payload)
		if _, err := DecodeTargetRecord(mustMarshal(t, bad)); err == nil {
			t.Fatalf("%s: decoded", name)
		}
	}
}

// A target that answers "complete" with anything but the credential this node
// computed, signed by both identity keys, has completed nothing.
func TestInitiatorStoresOnlyTheCredentialItComputed(t *testing.T) {
	s, a, b := standardPair(t)
	ia, _ := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	c := s.addGenerated("C", 8)
	cred := ia.Transcript.Credential()
	other := credential.New(a.party.node, b.party.node, cred.CreatedAt, time.Time{}, credential.Nonce{7})
	otherDone, err := record.CoSign(credential.Descriptor, mustSeal(t, other, a.signer), b.signer)
	must(t, err)
	withStranger := credential.New(a.party.node, c.party.node, cred.CreatedAt, time.Time{}, cred.Nonce)
	strangerDone, err := record.CoSign(credential.Descriptor, mustSeal(t, withStranger, a.signer), c.signer)
	must(t, err)
	partial := mustSeal(t, cred, a.signer)
	for name, rec := range map[string]record.Record{"another credential": otherDone, "a credential with a third node": strangerDone, "only the initiator's signature": partial} {
		rec := rec
		frame := mustEncode(t, ResultMsg{ID: ia.ID(), Status: StatusComplete, Credential: &rec})
		next, actions, err := a.state.OnFetchResult(a.world(), ia.ID(), b.party, frame, a.rng, s.now)
		must(t, err)
		for _, act := range actions {
			if _, stored := act.(StoreCredential); stored {
				t.Fatalf("%s: stored", name)
			}
		}
		if r, _ := next.Initiator(ia.ID()); r.Status != InitiatorActive {
			t.Fatalf("%s: record %v", name, r.Status)
		}
	}
}

func TestOnePairingAtATimePerTarget(t *testing.T) {
	s, a, b := standardPair(t)
	ia, _ := startPairing(t, s, a, b)
	_, out := s.initiate(a, b, Initiation{})
	if out.Result != ResultInProgress {
		t.Fatalf("second initiation while one is active: %+v", out)
	}
	// Withdrawn, a new one may start.
	a.reject(ia.ID(), WithdrawDeclined)
	s.run(time.Minute, 5*time.Second)
	if _, out := s.initiate(a, b, Initiation{}); out.Result != ResultOK {
		t.Fatalf("initiation after the withdrawal: %+v", out)
	}
}

func TestDecodingRefusesAnInconsistentInitiatorRecord(t *testing.T) {
	s, a, b := standardPair(t)
	ia, _ := startPairing(t, s, a, b)
	var stored envelope[initiatorWire]
	must(t, record.UnmarshalStrict(a.disk["initiator/"+hexID(ia.ID())], &stored, MaxPersisted))
	for name, mutate := range map[string]func(*initiatorWire){
		"complete without a credential": func(w *initiatorWire) { w.Status = uint64(InitiatorComplete); e := int64(5); w.EndedAt = &e },
		"released without a signature":  func(w *initiatorWire) { w.Released = true },
		"withdrawing without a reason":  func(w *initiatorWire) { w.Status = uint64(InitiatorWithdrawing); e := int64(5); w.EndedAt = &e },
		"an address of length zero":     func(w *initiatorWire) { w.Addresses = []string{""} },
	} {
		bad := stored
		mutate(&bad.Payload)
		if _, err := DecodeInitiatorRecord(mustMarshal(t, bad)); err == nil {
			t.Fatalf("%s: decoded", name)
		}
	}
}

func FuzzParseCode(f *testing.F) {
	for _, s := range []string{"482 019", "482019", "000 000", "", "1234567", "abc def"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		c, err := ParseCode(in)
		if err != nil {
			return
		}
		if c > 999999 {
			t.Fatalf("code %d out of range", c)
		}
		if again, err := ParseCode(c.String()); err != nil || again != c {
			t.Fatalf("text form does not round trip: %q", c.String())
		}
	})
}

// The initiator's signature is kept by the target when it arrives, before its
// owner has confirmed, so a restart does not lose it.
func TestTargetKeepsTheInitiatorSignatureAcrossARestart(t *testing.T) {
	s, a, b := standardPair(t)
	ia, tb := startPairing(t, s, a, b)
	a.confirm(ia.ID(), ia.Transcript.Code())
	s.run(10*time.Second, time.Second)
	b.restart()
	if r, _ := b.state.Target(tb.ID()); r.InitiatorSignature == nil {
		t.Fatal("the signature did not survive the restart")
	}
	b.confirm(tb.ID(), tb.Transcript.Code()) // completes at once, without asking A again
	if len(b.credentials) != 1 {
		t.Fatal("not completed from the kept signature")
	}
}
