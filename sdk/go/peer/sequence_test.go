package peer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/peer"
)

// A stateful test of the peer's request bookkeeping. A script of operations
// (requests that finish at once or wait for a gate, requests for a method that
// does not exist, requests with bad params, duplicates of an id that is in
// flight, cancellations, notifications, malformed lines, stray responses, a
// close) is played against a peer with small queues over a raw connection. A
// model that does not use the peer package keeps what was sent and checks, from
// the lines the peer wrote and from what its handlers saw:
//
//   - every well-formed request is answered exactly once, whatever else happens;
//   - a request answered -32001 never reached its handler, and no handler ran
//     twice for one request;
//   - a duplicate of an id in flight is answered -32600 and never runs;
//   - a reply of -32800 only follows a cancel for that request, or the close of
//     the peer, which cancels every handler's context so that a handler that
//     returns ctx.Err() is answered -32800 and the answer can win the race with
//     the transport being closed;
//   - a malformed line gets one error with a null id, and a notification, a stray
//     response or a cancel gets no reply at all.
//
// The same player runs scripts from a fixed seed, with counts of each path it
// took, and scripts the fuzzer invents.

type seqOp uint8

const (
	opFast        seqOp = iota // a request that returns at once
	opGate                     // a request whose handler waits for the gate or its cancellation
	opUnknown                  // a request for a method nobody handles
	opBadParams                // a request whose params are a scalar
	opDuplicate                // a request with the id of one that is in flight
	opCancel                   // $/cancel_request for some id
	opNote                     // a notification with a handler
	opUnknownNote              // a notification without one
	opBadNote                  // a notification whose params are a scalar
	opMalformed                // a line that is no valid message
	opStray                    // a response nobody is waiting for
	opRelease                  // open the gate
	opClose                    // close the peer; ends the script
	numSeqOps
)

var seqOpNames = [numSeqOps]string{"fast", "gate", "unknown", "bad params", "duplicate", "cancel", "note", "unknown note",
	"bad note", "malformed", "stray response", "release", "close"}

type seqStep struct {
	op  seqOp
	arg uint8
}

// malformedLines are lines that are no valid message; each earns one error with a
// null id. Some may be a response that was meant for the other side: they are
// never answered with the id they carry.
var malformedLines = []string{
	`not json`,
	`[1,2]`,
	`"text"`,
	`{"jsonrpc":"2.0","id":5}`,
	`{"id":3,"result":{}}`,
	`{"jsonrpc":"1.0","id":4,"error":{"code":1001,"message":"x"}}`,
	`{"jsonrpc":"2.0","id":1,"id":2,"method":"fast"}`,
	`{"jsonrpc":"2.0","method":"fast","method":"gate"}`,
}

// seqRequest is what the model knows of one request line it sent.
type seqRequest struct {
	seq       int
	id        string // the id as written on the wire
	op        seqOp
	cancelled bool // a cancel for its id was sent after it
	duplicate bool // the line repeated the id of a request in flight
}

// seqPeer is the code under test together with what its handlers saw.
type seqPeer struct {
	mu      sync.Mutex
	began   map[int]int // seq -> handlers started for it
	gate    chan struct{}
	gateOpn bool
}

func (s *seqPeer) begin(seq int) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.began[seq]++
	return s.gate
}

func (s *seqPeer) started(seq int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.began[seq]
}

func seqOf(params json.RawMessage) int {
	var in struct {
		T int `json:"t"`
	}
	_ = json.Unmarshal(params, &in)
	return in.T
}

// seqCoverage counts the paths the scripts took.
type seqCoverage map[string]int

func (c seqCoverage) add(o seqCoverage) {
	for k, v := range o {
		c[k] += v
	}
}

// playScript runs one script and returns the paths it took. It fails the test on
// the first broken property.
func playScript(t *testing.T, script []seqStep) seqCoverage {
	t.Helper()
	cov := seqCoverage{}
	sp := &seqPeer{began: map[int]int{}, gate: make(chan struct{})}
	var notes int
	var notesMu sync.Mutex
	// Small queues, so that overload is common: two handlers run, three requests
	// wait, the sixth is refused. The notification queue is larger than any script.
	p, r, _ := newRaw(t, peer.Params{MaxPendingRequests: 3, MaxConcurrentHandlers: 2, MaxPendingNotifications: 512}, func(p *peer.Peer) {
		p.Handle("gate", func(ctx context.Context, params json.RawMessage) (any, error) {
			gate := sp.begin(seqOf(params))
			select {
			case <-gate:
				return struct{}{}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
		p.Handle("fast", func(_ context.Context, params json.RawMessage) (any, error) {
			seq := seqOf(params)
			sp.begin(seq)
			return map[string]int{"t": seq}, nil
		})
		p.HandleNotification("note", func(context.Context, json.RawMessage) {
			notesMu.Lock()
			notes++
			notesMu.Unlock()
		})
	})

	// Every line the peer writes, in order.
	var repMu sync.Mutex
	var replies []frame
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			line, err, ok := r.readLine(time.Hour)
			if !ok || err != nil {
				return
			}
			var f frame
			if json.Unmarshal(line, &f) != nil {
				panic(fmt.Sprintf("the peer wrote a line that is not JSON: %q", line))
			}
			repMu.Lock()
			replies = append(replies, f)
			repMu.Unlock()
		}
	}()
	snapshot := func() []frame {
		repMu.Lock()
		defer repMu.Unlock()
		return append([]frame(nil), replies...)
	}
	repliesTo := func(id string) []frame {
		var out []frame
		for _, f := range snapshot() {
			if string(f.ID) == id {
				out = append(out, f)
			}
		}
		return out
	}
	waitUntil := func(what string, cond func() bool) bool {
		deadline := time.Now().Add(wait)
		for !cond() {
			if time.Now().After(deadline) {
				t.Errorf("timed out waiting for %s", what)
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}

	var (
		reqs           []*seqRequest    // every request line with a valid id, in order
		released       = map[int]bool{} // gate requests sent before a release: not duplicate targets any more
		malformed      int
		closedEarly    bool
		repliesAtClose int // replies seen before Close was called: later ones may be answers to the close
		sentIDs        []string
		lastDupOfSeq   = map[string]int{}
	)
	nextSeq := 0
	newID := func(seq int) string {
		if seq%2 == 0 {
			return strconv.Itoa(seq)
		}
		return `"r` + strconv.Itoa(seq) + `"`
	}
	sendRequest := func(op seqOp, method string, params string, id string) *seqRequest {
		nextSeq++
		seq := nextSeq
		if id == "" {
			id = newID(seq)
		}
		rq := &seqRequest{seq: seq, id: id, op: op}
		reqs = append(reqs, rq)
		if params == "" {
			params = fmt.Sprintf(`{"t":%d}`, seq)
		}
		r.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"%s","params":%s}`, id, method, params))
		return rq
	}

	// barrier returns once the read loop has handled every line sent so far: it
	// sends a request that the read loop answers by itself (nobody handles the
	// method) and waits for the answer. Without it a gate could be opened while an
	// earlier duplicate was still unread, and the duplicate would find its id free.
	barrier := func() {
		rq := sendRequest(opUnknown, "no.such.method", "", "")
		waitUntil("the read loop to catch up", func() bool { return len(repliesTo(rq.id)) > 0 })
	}

	for _, st := range script {
		if closedEarly {
			break
		}
		cov["op "+seqOpNames[st.op]]++
		switch st.op {
		case opFast:
			sentIDs = append(sentIDs, sendRequest(opFast, "fast", "", "").id)
		case opGate:
			sentIDs = append(sentIDs, sendRequest(opGate, "gate", "", "").id)
		case opUnknown:
			sentIDs = append(sentIDs, sendRequest(opUnknown, "no.such.method", "", "").id)
		case opBadParams:
			sentIDs = append(sentIDs, sendRequest(opBadParams, "fast", "5", "").id)
		case opDuplicate:
			// Only a handler that has started and not been released or cancelled
			// holds its id for certain.
			var candidates []*seqRequest
			for _, rq := range reqs {
				if rq.op == opGate && !rq.cancelled && !rq.duplicate && !released[rq.seq] && sp.started(rq.seq) > 0 &&
					len(repliesTo(rq.id)) == 0 {
					candidates = append(candidates, rq)
				}
			}
			if len(candidates) == 0 {
				cov["duplicate without a candidate"]++
				continue
			}
			target := candidates[int(st.arg)%len(candidates)]
			dup := sendRequest(opDuplicate, "fast", "", target.id)
			dup.duplicate = true
			lastDupOfSeq[dup.id]++
		case opCancel:
			var id string
			if len(sentIDs) == 0 || st.arg%5 == 0 {
				id = "99999" // a request that was never sent
				cov["cancel of an unknown id"]++
			} else {
				id = sentIDs[int(st.arg)%len(sentIDs)]
			}
			r.send(fmt.Sprintf(`{"jsonrpc":"2.0","method":"$/cancel_request","params":{"request_id":%s}}`, id))
			for _, rq := range reqs {
				if rq.id == id && !rq.duplicate {
					rq.cancelled = true
					// A running handler ends with -32800 at once; wait for it so that
					// its id is known to be free again.
					if sp.started(rq.seq) > 0 && rq.op == opGate {
						waitUntil("the reply to a cancelled request", func() bool { return len(repliesTo(rq.id)) > lastDupOfSeq[rq.id] })
					}
				}
			}
		case opNote:
			r.send(`{"jsonrpc":"2.0","method":"note","params":{}}`)
		case opUnknownNote:
			r.send(`{"jsonrpc":"2.0","method":"no.such.note","params":{}}`)
		case opBadNote:
			r.send(`{"jsonrpc":"2.0","method":"note","params":7}`)
		case opMalformed:
			r.send(malformedLines[int(st.arg)%len(malformedLines)])
			cov["malformed "+strconv.Itoa(int(st.arg)%len(malformedLines))]++
			malformed++
		case opStray:
			r.send(`{"jsonrpc":"2.0","id":987654,"result":{}}`)
		case opRelease:
			barrier()
			sp.mu.Lock()
			close(sp.gate)
			sp.gate = make(chan struct{})
			sp.mu.Unlock()
			for _, rq := range reqs {
				if rq.op == opGate {
					released[rq.seq] = true
				}
			}
		case opClose:
			closedEarly = true
			cov["early close"]++
			repliesAtClose = len(snapshot())
			p.Close()
		}
	}

	// owed counts the replies owed per id for the requests sent so far.
	owed := func() map[string]int {
		m := map[string]int{}
		for _, rq := range reqs {
			m[rq.id]++
		}
		return m
	}

	if !closedEarly {
		// Let everything finish: open the gate for good, then wait until every
		// request and malformed line has its reply.
		barrier()
		sp.mu.Lock()
		if !sp.gateOpn {
			close(sp.gate)
			sp.gateOpn = true
		}
		sp.mu.Unlock()
		total := malformed
		for _, n := range owed() {
			total += n
		}
		if !waitUntil("every owed reply", func() bool { return len(snapshot()) >= total }) {
			t.Logf("owed %d replies, have %d", total, len(snapshot()))
		}
		// A sentinel request goes through the same queues behind everything above.
		sentinel := sendRequest(opFast, "fast", "", "")
		waitUntil("the sentinel's reply", func() bool { return len(repliesTo(sentinel.id)) > 0 })
		p.Close()
	}
	waitDone(t, p)
	<-done
	expected := owed()

	// Nothing may start after the peer has closed.
	began := map[int]int{}
	sp.mu.Lock()
	for k, v := range sp.began {
		began[k] = v
	}
	sp.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	for seq, n := range began {
		if got := sp.started(seq); got != n {
			t.Errorf("request %d: a handler started after the peer closed", seq)
		}
	}

	all := snapshot()
	nullReplies := 0
	perID := map[string][]frame{}
	cancelledByClose := map[string]bool{} // ids whose -32800 arrived after Close was called
	for i, f := range all {
		if f.Error == nil && f.Result == nil {
			t.Errorf("the peer wrote %+v, neither a result nor an error", f)
		}
		if string(f.ID) == "null" {
			nullReplies++
			if f.errCode() != peer.CodeParseError && f.errCode() != peer.CodeInvalidRequest {
				t.Errorf("a reply with a null id carries %+v", f)
			}
			continue
		}
		perID[string(f.ID)] = append(perID[string(f.ID)], f)
		if closedEarly && i >= repliesAtClose && f.errCode() == peer.CodeRequestCancelled {
			cancelledByClose[string(f.ID)] = true
		}
	}

	// Malformed lines: one error each; nothing else is answered with a null id.
	if !closedEarly && nullReplies != malformed {
		t.Errorf("%d malformed lines were answered by %d null-id errors", malformed, nullReplies)
	}
	if nullReplies > malformed {
		t.Errorf("%d null-id errors for %d malformed lines", nullReplies, malformed)
	}

	for id, fs := range perID {
		if expected[id] == 0 {
			t.Errorf("the peer answered id %s, which no request used: %+v", id, fs)
		}
	}
	for _, rq := range reqs {
		if rq.duplicate {
			continue
		}
		fs := perID[rq.id]
		var original []frame
		dupReplies := 0
		for _, f := range fs {
			if f.errCode() == peer.CodeInvalidRequest {
				dupReplies++
			} else {
				original = append(original, f)
			}
		}
		dups := lastDupOfSeq[rq.id]
		if !closedEarly {
			if len(original) != 1 {
				t.Errorf("request %d (%s, id %s) was answered %d times apart from duplicate rejections: %+v", rq.seq, seqOpNames[rq.op], rq.id, len(original), fs)
				continue
			}
			if dupReplies != dups {
				t.Errorf("request %d: %d duplicates of its id were sent, %d were rejected", rq.seq, dups, dupReplies)
			}
		} else if len(original) > 1 || dupReplies > dups {
			t.Errorf("request %d was answered more than once after a close: %+v", rq.seq, fs)
		}
		if len(original) == 0 {
			continue
		}
		f := original[0]
		ran := sp.started(rq.seq)
		if ran > 1 {
			t.Errorf("request %d: its handler started %d times", rq.seq, ran)
		}
		switch f.errCode() {
		case 0: // a result
			cov["result"]++
			if rq.op == opUnknown || rq.op == opBadParams {
				t.Errorf("request %d (%s) got a result: %+v", rq.seq, seqOpNames[rq.op], f)
			}
			if ran != 1 {
				t.Errorf("request %d got a result but its handler started %d times", rq.seq, ran)
			}
			if rq.op == opFast && string(f.Result) != fmt.Sprintf(`{"t":%d}`, rq.seq) {
				t.Errorf("request %d got the result %s", rq.seq, f.Result)
			}
		case peer.CodeOverloaded:
			cov["overloaded"]++
			if ran != 0 {
				t.Errorf("request %d was refused as overloaded but its handler ran", rq.seq)
			}
		case peer.CodeRequestCancelled:
			cov["cancelled"]++
			if cancelledByClose[rq.id] {
				cov["cancelled by close"]++
			} else if !rq.cancelled {
				t.Errorf("request %d was answered -32800 without a cancel", rq.seq)
			}
			if ran == 0 {
				cov["cancelled while queued"]++
			}
		case peer.CodeMethodNotFound:
			cov["method not found"]++
			if rq.op != opUnknown || ran != 0 {
				t.Errorf("request %d (%s) got -32601, handler started %d times", rq.seq, seqOpNames[rq.op], ran)
			}
		case peer.CodeInvalidParams:
			cov["invalid params"]++
			if rq.op != opBadParams || ran != 0 {
				t.Errorf("request %d (%s) got -32602, handler started %d times", rq.seq, seqOpNames[rq.op], ran)
			}
		default:
			t.Errorf("request %d got %+v", rq.seq, f)
		}
		if (rq.op == opUnknown && f.errCode() != peer.CodeMethodNotFound && f.errCode() != peer.CodeOverloaded && f.errCode() != peer.CodeRequestCancelled) ||
			(rq.op == opBadParams && f.errCode() != peer.CodeInvalidParams && f.errCode() != peer.CodeOverloaded && f.errCode() != peer.CodeRequestCancelled) {
			t.Errorf("request %d (%s) got %+v", rq.seq, seqOpNames[rq.op], f)
		}
	}
	for _, rq := range reqs {
		if rq.duplicate {
			if got := sp.started(rq.seq); got != 0 {
				t.Errorf("the duplicate %d ran its handler", rq.seq)
			}
		}
	}
	for _, n := range lastDupOfSeq {
		cov["duplicate rejected"] += n
	}
	notesMu.Lock()
	if closedEarly == false && notes > cov["op note"]+cov["op bad note"] {
		t.Errorf("%d notifications handled, %d sent", notes, cov["op note"])
	}
	notesMu.Unlock()
	return cov
}

// randomScript draws a script from the generator.
func randomScript(rng *rand.Rand, length int, closeAtEnd bool) []seqStep {
	// Gate requests are common, so that the queue fills and refuses requests.
	weights := []struct {
		op seqOp
		w  int
	}{
		{opFast, 10}, {opGate, 22}, {opUnknown, 4}, {opBadParams, 4}, {opDuplicate, 8}, {opCancel, 12},
		{opNote, 5}, {opUnknownNote, 3}, {opBadNote, 3}, {opMalformed, 8}, {opStray, 3}, {opRelease, 6},
	}
	total := 0
	for _, w := range weights {
		total += w.w
	}
	var script []seqStep
	for i := 0; i < length; i++ {
		n := rng.Intn(total)
		for _, w := range weights {
			if n < w.w {
				script = append(script, seqStep{w.op, uint8(rng.Intn(256))})
				break
			}
			n -= w.w
		}
	}
	if closeAtEnd {
		script = append(script, seqStep{opClose, 0})
	}
	return script
}

func TestRandomOperationSequences(t *testing.T) {
	leakGuard(t)
	cov := seqCoverage{}
	const scripts = 150
	for seed := int64(1); seed <= scripts; seed++ {
		rng := rand.New(rand.NewSource(20261023 + seed))
		script := randomScript(rng, 25+rng.Intn(40), seed%5 == 0)
		if !t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			cov.add(playScript(t, script))
		}) {
			return // one broken script is enough; the next ones would only wait for timeouts
		}
	}
	// The scripts must have gone through the paths the properties are about.
	need := map[string]int{
		"result": 100, "overloaded": 40, "cancelled": 20, "cancelled while queued": 3, "method not found": 10, "invalid params": 10,
		"duplicate rejected": 15, "early close": 10, "op note": 20, "op unknown note": 10, "op bad note": 10, "op stray response": 10,
		"cancel of an unknown id": 5,
	}
	for i := range malformedLines {
		need["malformed "+strconv.Itoa(i)] = 5
	}
	for what, n := range need {
		if cov[what] < n {
			t.Errorf("the scripts took %q only %d times, want at least %d", what, cov[what], n)
		}
	}
	var names []string
	for k, v := range cov {
		names = append(names, fmt.Sprintf("%s=%d", k, v))
	}
	t.Logf("paths taken: %s", strings.Join(names, " "))
}

// scriptFromBytes reads a script from fuzzer input: two bytes per step.
func scriptFromBytes(data []byte) []seqStep {
	var script []seqStep
	for i := 0; i+1 < len(data) && len(script) < 80; i += 2 {
		script = append(script, seqStep{seqOp(data[i] % uint8(numSeqOps)), data[i+1]})
	}
	return script
}

func FuzzOperationSequence(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{byte(opGate), 0, byte(opGate), 0, byte(opDuplicate), 0, byte(opRelease), 0, byte(opFast), 0})
	f.Add([]byte{byte(opGate), 0, byte(opGate), 0, byte(opGate), 0, byte(opGate), 0, byte(opGate), 0, byte(opGate), 0, byte(opGate), 0, byte(opGate), 0, byte(opCancel), 5})
	f.Add([]byte{byte(opGate), 1, byte(opCancel), 1, byte(opMalformed), 3, byte(opStray), 0, byte(opNote), 0, byte(opClose), 0})
	f.Add([]byte{byte(opFast), 0, byte(opUnknown), 0, byte(opBadParams), 0, byte(opMalformed), 6, byte(opMalformed), 7})
	f.Fuzz(func(t *testing.T, data []byte) {
		playScript(t, scriptFromBytes(data))
	})
}
