package engine

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

func refs(dir string, n int) []store.FileRef {
	var out []store.FileRef
	for i := 0; i < n; i++ {
		out = append(out, store.FileRef{Dir: dir, Name: fmt.Sprintf("%s-%d", dir, i)})
	}
	return out
}

// A chunk is cut from the front in the order the store removes the groups, so the
// chunks that follow one another keep what the store relies on: a control record
// before the delivery derived from it, and an endpoint after what hangs on it.
func TestCutSweepTakesAtMostTheBoundInTheOrderOfTheStoreAndLosesNothing(t *testing.T) {
	whole := store.SweepChunk{
		Ctl: refs("ctl", 2), Records: refs("in", 3), Outs: []store.OutRecord{outRec(t, 1), outRec(t, 2)},
		OutLosts: []store.OutLostRecord{{}, {}}, Endpoints: refs("endpoints", 2),
	}
	total := 2 + 3 + 2 + 2 + 2
	var order []string
	rest, steps := whole, 0
	for !sweepEmpty(rest) {
		var head store.SweepChunk
		head, rest = cutSweep(rest, 4)
		n := len(head.Ctl) + len(head.Records) + len(head.Outs) + len(head.OutLosts) + len(head.Endpoints)
		if n == 0 || n > 4 {
			t.Fatalf("chunk %d holds %d files, want between 1 and 4", steps, n)
		}
		for _, f := range head.Ctl {
			order = append(order, f.Name)
		}
		for _, f := range head.Records {
			order = append(order, f.Name)
		}
		for range head.Outs {
			order = append(order, "out")
		}
		for range head.OutLosts {
			order = append(order, "out-lost")
		}
		for _, f := range head.Endpoints {
			order = append(order, f.Name)
		}
		if steps++; steps > total {
			t.Fatal("the cutting does not end")
		}
	}
	want := []string{"ctl-0", "ctl-1", "in-0", "in-1", "in-2", "out", "out", "out-lost", "out-lost", "endpoints-0", "endpoints-1"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("the files went in the order\n got  %v\n want %v", order, want)
	}
}

// The plan was made from a scan some commands ago, and a direct endpoint keeps its
// identifier for ever: one that was closed then and is registered again by the time
// the plan is run is not removed, and nor is anything else the plan has left, because
// the files that hang on it are named in chunks of their own.
func TestAPlanWithAnEndpointThatIsOpenAgainIsStale(t *testing.T) {
	closed := store.EndpointRecord{ID: idOf(t, 1), Status: store.EndpointClosed}
	reopened := store.EndpointRecord{ID: idOf(t, 2), Status: store.EndpointClosed}
	gone := store.EndpointRecord{ID: idOf(t, 3), Status: store.EndpointClosed}
	ref := func(r store.EndpointRecord) store.FileRef {
		return store.FileRef{Dir: store.EndpointsDir, Name: r.Name()}
	}
	reopenedNow := reopened
	reopenedNow.Status = store.EndpointOpen
	e := &Engine{view: &view{endpoints: map[ident.EndpointID]*endpointState{
		closed.ID: {rec: closed}, reopened.ID: {rec: reopenedNow},
	}}}
	records := map[store.FileRef]store.Record{ref(closed): closed, ref(reopened): reopened, ref(gone): gone}
	for _, c := range []struct {
		rule      string
		endpoints []store.FileRef
		stale     bool
	}{
		{"endpoints that are all still closed", []store.FileRef{ref(closed)}, false},
		{"an endpoint the view no longer holds", []store.FileRef{ref(closed), ref(gone)}, false},
		{"an endpoint that is open again", []store.FileRef{ref(reopened)}, true},
		{"an endpoint that is open again among others", []store.FileRef{ref(closed), ref(reopened), ref(gone)}, true},
		{"a plan with no endpoint", nil, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			s := &sweepPass{records: records, chunk: store.SweepChunk{Endpoints: c.endpoints}}
			if got := e.planIsStale(s); got != c.stale {
				t.Errorf("planIsStale = %v, want %v", got, c.stale)
			}
		})
	}
}

// What the sweep removes leaves the view by the record it was, and nothing it did not
// remove goes with it.
func TestForgetSweptTakesTheRemovedRecordsOutOfTheView(t *testing.T) {
	ep := idOf(t, 1)
	kept, swept := inRec(t, 1, ep, delivery.StateAccepted), inRec(t, 2, ep, delivery.StateDelivered)
	keptOut, sweptOut := outRec(t, 1, delivery.StateQueued), outRec(t, 2, delivery.StateDelivered)
	v := &view{
		in: map[store.TargetKey]store.InRecord{}, ctl: map[store.TargetKey]store.CtlRecord{}, out: map[string]store.OutRecord{},
		queued: map[ident.EndpointID]map[store.TargetKey]bool{}, backlog: map[ident.EndpointID]int{},
		received: map[msgKey][]store.TargetKey{}, arrived: map[arrivalKey][]store.TargetKey{},
		byMessage: map[msgKey]string{}, idem: map[idemKey]string{}, endpoints: map[ident.EndpointID]*endpointState{},
	}
	var ticked []string
	v.onTick = func(record string, next time.Time) {
		if next.IsZero() {
			ticked = append(ticked, record)
		}
	}
	for _, in := range []store.InRecord{kept, swept} {
		v.setIn(in)
	}
	for _, o := range []store.OutRecord{keptOut, sweptOut} {
		v.setOut(o)
	}
	lost := func(n int) store.OutLostRecord {
		return store.NewOutLostRecord(store.OutLostKey{Sender: ep, Damaged: strings.Repeat(fmt.Sprintf("%x", n), 64)}, nodeOf(t, 1), time.Time{})
	}
	sweptLost, keptLost := lost(1), lost(2)
	// A session endpoint and a direct one are swept: the book forgets the pairs of the
	// first, whose identifier is never used again, and keeps those of the second.
	sessionEp := store.EndpointRecord{ID: idOf(t, 8), Kind: adapterreg.KindSession, Status: store.EndpointClosed}
	directEp := store.EndpointRecord{ID: idOf(t, 9), Kind: adapterreg.KindDirect, Status: store.EndpointClosed}
	target := ident.Address{Node: nodeOf(t, 2), Endpoint: idOf(t, 7)}
	v.seq, v.tokens = store.NewSeqBook(&store.Scan{}), newTokenIndex()
	v.seq.Note(sessionEp.ID, target, 4)
	v.seq.Note(directEp.ID, target, 5)
	v.seq.Note(ep, target, 6)
	v.endpoints[sessionEp.ID], v.endpoints[directEp.ID] = &endpointState{rec: sessionEp}, &endpointState{rec: directEp}
	// The outbox holds the state of both source records, and the pull before the close
	// of the deliveries of both: one of the swept record's deliveries became final by a
	// report, so no tick cleared its entry.
	outbox := newOutbox()
	for _, o := range []store.OutRecord{keptOut, sweptOut} {
		outbox.state[o.Name()] = &outState{}
		for _, d := range o.Outbound.Deliveries() {
			outbox.finalPulled[finalPullKey{o.Name(), d.Address.Endpoint}] = struct{}{}
		}
	}
	if len(outbox.finalPulled) != 2 {
		t.Fatalf("the scenario holds %d entries of finalPulled, want one for each source record", len(outbox.finalPulled))
	}
	e := &Engine{log: slog.New(slog.DiscardHandler), view: v, outbox: outbox, fb: feedbackState{outLost: map[string]store.OutLostRecord{sweptLost.Name(): sweptLost, keptLost.Name(): keptLost}}}
	s := &sweepPass{records: map[store.FileRef]store.Record{
		{Dir: store.InDir, Name: swept.Name()}:            swept,
		{Dir: store.EndpointsDir, Name: sessionEp.Name()}: sessionEp,
		{Dir: store.EndpointsDir, Name: directEp.Name()}:  directEp,
	}}
	ticked = nil
	e.forgetSwept(s, store.SweepChunk{
		Records: []store.FileRef{{Dir: store.InDir, Name: swept.Name()}}, Outs: []store.OutRecord{sweptOut},
		OutLosts:  []store.OutLostRecord{sweptLost},
		Endpoints: []store.FileRef{{Dir: store.EndpointsDir, Name: sessionEp.Name()}, {Dir: store.EndpointsDir, Name: directEp.Name()}},
	})
	if _, ok := outbox.state[sweptOut.Name()]; ok {
		t.Error("the outbox still holds the state of the swept source record")
	}
	if _, ok := outbox.state[keptOut.Name()]; !ok {
		t.Error("the outbox lost the state of a source record the sweep did not remove")
	}
	for k := range outbox.finalPulled {
		if k.name == sweptOut.Name() {
			t.Errorf("the outbox still holds the pull before the close of a delivery of the swept source record: %+v", k)
		}
	}
	if len(outbox.finalPulled) != 1 {
		t.Errorf("finalPulled holds %d entries, want the one of the source record that stays", len(outbox.finalPulled))
	}
	if got := v.seq.High(sessionEp.ID, target); got != 0 {
		t.Errorf("the book still holds a number of the swept session endpoint: %d", got)
	}
	if got := v.seq.High(directEp.ID, target); got != 5 {
		t.Errorf("the book forgot a direct endpoint's number, now %d, want 5", got)
	}
	if got := v.seq.High(ep, target); got != 6 {
		t.Errorf("the book forgot the number of a sender the sweep did not touch, now %d, want 6", got)
	}
	if _, ok := v.in[swept.Key]; ok {
		t.Error("the swept target record is still in the view")
	}
	if _, ok := v.in[kept.Key]; !ok {
		t.Error("a target record the sweep did not remove left the view")
	}
	if _, ok := v.out[sweptOut.Name()]; ok {
		t.Error("the swept source record is still in the view")
	}
	if _, ok := v.out[keptOut.Name()]; !ok {
		t.Error("a source record the sweep did not remove left the view")
	}
	if _, ok := v.byMessage[msgKey{sweptOut.Key.Sender, sweptOut.Key.Message}]; ok {
		t.Error("the index of the swept source record is still in the view")
	}
	if _, ok := e.fb.outLost[sweptLost.Name()]; ok {
		t.Error("the swept out-lost record is still held by the queue of feedback")
	}
	if _, ok := e.fb.outLost[keptLost.Name()]; !ok {
		t.Error("an out-lost record the sweep did not remove left the queue of feedback")
	}
	if v.unfinished != 2 {
		t.Errorf("unfinished = %d, want the 2 of the records that stay (one target, one source delivery)", v.unfinished)
	}
	if len(ticked) != 2 {
		t.Errorf("the schedule was told to clear %v, want an entry for each swept record", ticked)
	}
}
