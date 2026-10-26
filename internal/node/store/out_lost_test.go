package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// An out-lost record tells the sending endpoint that one of its messages was lost
// with a file nothing could be read from. It is named after the damaged file
// that caused it, holds one feedback item until a notice covers it, and is swept
// once the item is gone.

// damagedHash is the hash part of the name of a damaged file i of out/.
func damagedHash(i int) string { return strings.Repeat(fmt.Sprintf("%x", i), 64) }

// lostNotice is the out-lost record the settlement of the damaged file i of the
// sender numbered sender writes, discovered at at on the local node.
func lostNotice(sender, i int, at time.Time) store.OutLostRecord {
	return store.NewOutLostRecord(store.OutLostKey{Sender: ep(sender), Damaged: damagedHash(i)}, node(local), at)
}

// noticeForLost is the notice that covers the feedback item of an out-lost record.
func noticeForLost(o store.OutLostRecord) store.InRecord {
	n := noticeFor(store.OutRecord{Key: store.OutKey{Sender: o.Key.Sender, Message: msgID(7)}})
	n.Key.Message = "notice-" + delivery.MessageID(o.Key.Damaged[:8])
	n.Key.Recipient = o.Key.Sender
	n.Dedup = delivery.NewDedupRecord(n.Key.Dedup(), digestOf(99), n.Record.ReportBy)
	n.Covers = []store.CoverRef{{Out: o.Name(), Kind: delivery.KindLostUnknownMessage}}
	return n
}

func TestAnOutLostRecordIsNamedAfterItsDamagedFile(t *testing.T) {
	at := base.Add(time.Hour)
	o := lostNotice(1, 3, at)
	key := e1 + " " + damagedHash(3)
	sum := sha256.Sum256([]byte("out-lost " + key))
	want := e1 + "." + hex.EncodeToString(sum[:]) + ".rec"
	if got := o.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if b, err := o.Key.Bytes(); err != nil || string(b) != key {
		t.Errorf("Bytes() = %q, %v, want %q", b, err, key)
	}
	if o.Key.Kind() != store.KindOutLost || store.KindOutLost.Dir() != store.OutDir {
		t.Errorf("kind %q in %q", o.Key.Kind(), store.KindOutLost.Dir())
	}
	// The item is the one the delivery package builds, held by this record.
	wantItem := delivery.NewLostUnknownMessageItem(ident.Address{Node: node(local), Endpoint: ep(1)}, want, at)
	if len(o.Feedback) != 1 || o.Feedback[0] != wantItem || o.DiscoveredAt != at {
		t.Errorf("record %#v, want one item %+v discovered at %v", o, wantItem, at)
	}
	// The name derives from the damaged file alone: the same damaged file at
	// another time, by another node, is the same record.
	again := store.NewOutLostRecord(o.Key, node(3), at.Add(time.Hour))
	if again.Name() != want {
		t.Errorf("the name depends on the time or the node: %q", again.Name())
	}
	if lostNotice(1, 4, at).Name() == want || lostNotice(2, 3, at).Name() == want {
		t.Error("another damaged file, or another sender, has the same name")
	}
	data := mustEncode(t, o)
	back, dmg := store.DecodeRecord(store.OutDir, want, data)
	if dmg != nil {
		t.Fatalf("DecodeRecord: %+v", dmg)
	}
	if !equalRecords(back, o) {
		t.Errorf("round trip: got %#v want %#v", back, o)
	}
	cleared := o
	cleared.Feedback = nil
	if back, dmg := store.DecodeRecord(store.OutDir, want, mustEncode(t, cleared)); dmg != nil || !equalRecords(back, cleared) {
		t.Errorf("a record whose item was cleared: %v %+v", back, dmg)
	}
}

// A record without a valid key has no name: asking for it is a programming error
// and fails loudly instead of naming some other file.
func TestTheNameOfAnOutLostRecordWithoutAValidKeyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Name() of a zero record did not panic")
		}
	}()
	_ = store.OutLostRecord{}.Name()
}

func TestAnOutLostRecordIsRefusedWhereItIsNotOneAndEncodedOnlyWhole(t *testing.T) {
	good := lostNotice(1, 3, base)
	two := good
	two.Feedback = append(two.Feedback, good.Feedback[0])
	other := good
	other.Feedback = []delivery.Item{delivery.NewLostUnknownMessageItem(ident.Address{Node: node(local), Endpoint: ep(2)}, good.Name(), base)}
	otherSource := good
	otherSource.Feedback = []delivery.Item{delivery.NewLostUnknownMessageItem(good.Feedback[0].Ref.Sender, "elsewhere", base)}
	withMessage := good
	withMessage.Feedback = []delivery.Item{good.Feedback[0]}
	withMessage.Feedback[0].Ref.Message = "msg-0001"
	otherTime := good
	otherTime.Feedback = []delivery.Item{good.Feedback[0]}
	otherTime.Feedback[0].At = base.Add(time.Minute)
	noTime := good
	noTime.DiscoveredAt = time.Time{}
	badHash := good
	badHash.Key.Damaged = "abc"
	noSender := good
	noSender.Key.Sender = ident.EndpointID{}
	for name, rec := range map[string]store.OutLostRecord{
		"two items":                         two,
		"an item of another endpoint":       other,
		"an item that names another source": otherSource,
		"an item that names a message":      withMessage,
		"an item decided at another time":   otherTime,
		"no discovery time":                 noTime,
		"a damaged file that has no hash":   badHash,
		"no sender":                         noSender,
	} {
		if _, err := store.EncodeRecord(rec); err == nil {
			t.Errorf("%s: encoded", name)
		}
	}
}

func TestParseKeyOfAnOutLostRecord(t *testing.T) {
	key := e1 + " " + damagedHash(5)
	k, err := store.ParseKey(store.KindOutLost, []byte(key))
	if err != nil || k != (store.OutLostKey{Sender: ep(1), Damaged: damagedHash(5)}) {
		t.Fatalf("ParseKey = %v, %v", k, err)
	}
	for _, bad := range []string{
		e1,
		e1 + " " + damagedHash(5) + " " + damagedHash(5),
		e1 + " " + strings.ToUpper(damagedHash(10)),
		e1 + " " + damagedHash(5)[:63],
		"ep1x " + damagedHash(5),
		n2 + " " + damagedHash(5),
		"",
	} {
		if _, err := store.ParseKey(store.KindOutLost, []byte(bad)); err == nil {
			t.Errorf("ParseKey accepted %q", bad)
		}
	}
}

func TestScanListsTheOutLostRecordsApartFromTheSourceRecords(t *testing.T) {
	r := newRig(t)
	a, b := lostNotice(1, 1, base), lostNotice(2, 2, base)
	seed(t, r.mem, srcOut(1, false), a, b)
	sc := r.scan()
	if len(sc.Out) != 1 || len(sc.OutLost) != 2 || len(sc.Damaged)+len(sc.Unknown) != 0 {
		t.Fatalf("out %d out-lost %d damaged %v unknown %v", len(sc.Out), len(sc.OutLost), sc.Damaged, sc.Unknown)
	}
	got := map[string]bool{sc.OutLost[0].Name(): true, sc.OutLost[1].Name(): true}
	if !got[a.Name()] || !got[b.Name()] {
		t.Errorf("listed %v", got)
	}
	damageBody(t, r.mem, path.Join(store.OutDir, a.Name()))
	sc = r.scan()
	if len(sc.OutLost) != 1 || len(sc.Damaged) != 1 || sc.Damaged[0].Kind != store.KindOutLost {
		t.Errorf("a damaged out-lost record is a damaged file of kind out-lost: %v %v", sc.OutLost, sc.Damaged)
	}
	if k, ok := sc.Damaged[0].Key.(store.OutLostKey); !ok || k != a.Key {
		t.Errorf("its trusted key: %#v", sc.Damaged[0].Key)
	}
}

// A record is finished, and may go, once no feedback item is pending in it. It is
// not the retention of a source record: nothing waits for a reply, so it goes at
// the instant it was discovered at or later.
func TestAnOutLostRecordIsFinishedOnceItsFeedbackIsGone(t *testing.T) {
	at := base.Add(time.Hour)
	pending := lostNotice(1, 1, at)
	cleared := pending
	cleared.Feedback = nil
	if until, finished := store.RetainUntil(pending); finished || !until.Equal(at) {
		t.Errorf("a record with its item pending: %v, %v; want %v, not finished", until, finished, at)
	}
	if until, finished := store.RetainUntil(cleared); !finished || !until.Equal(at) {
		t.Errorf("a record whose item is gone: %v, %v; want %v, finished", until, finished, at)
	}
}

func TestPlanSweepTakesAnOutLostRecordOfAnOpenEndpointOnlyOnceItsFeedbackIsGone(t *testing.T) {
	at := base.Add(time.Hour)
	pending, cleared := lostNotice(1, 1, at), lostNotice(1, 2, at)
	cleared.Feedback = nil
	r := newRig(t)
	seed(t, r.mem, session(1, store.EndpointOpen, ctlName(4)), pending, cleared)
	plan := store.PlanSweep(r.scan(), at)
	if len(plan.OutLosts) != 1 || plan.OutLosts[0].Name() != cleared.Name() {
		t.Errorf("planned %v, want only the one whose item is gone", plan.OutLosts)
	}
	if len(plan.Outs)+len(plan.Records)+len(plan.Summaries) != 0 {
		t.Errorf("an out-lost record is no source record and has no summary: %+v", plan)
	}
	if early := store.PlanSweep(r.scan(), at.Add(-time.Nanosecond)); len(early.OutLosts) != 0 {
		t.Errorf("swept before its discovery time: %v", early.OutLosts)
	}
	mustDo(t, r.st.ArchiveSweep(plan.Chunk()))
	sc := r.scan()
	if len(sc.OutLost) != 1 || sc.OutLost[0].Name() != pending.Name() || len(sc.Seq) != 0 {
		t.Errorf("after the sweep: out-lost %v, floors %v; the pending one stays and a sweep of it writes no floor", sc.OutLost, sc.Seq)
	}
}

// A sweep that names an out-lost record removes exactly that file, and a sweep
// of one that is not there is not an error: a crash may have removed it.
func TestArchiveSweepRemovesOutLostRecordsAndToleratesOnesAlreadyGone(t *testing.T) {
	r := newRig(t)
	gone, kept := lostNotice(1, 1, base), lostNotice(1, 2, base)
	gone.Feedback, kept.Feedback = nil, nil
	seed(t, r.mem, gone)
	mustDo(t, r.st.ArchiveSweep(store.SweepChunk{OutLosts: []store.OutLostRecord{gone, kept}}))
	if present(t, r, store.OutDir, gone.Name()) {
		t.Error("the record is still there")
	}
	if err := r.st.ArchiveSweep(store.SweepChunk{OutLosts: []store.OutLostRecord{{}}}); err == nil {
		t.Error("a record that has no name was accepted")
	}
}

// Endpoint E is kept while an out-lost record names it as sender, as a source
// record does: the notice is addressed to it. The sweep that removes the record
// leaves the endpoint, and the next one takes it.
func TestAClosedSessionEndpointIsKeptWhileAnOutLostRecordNamesItAsSender(t *testing.T) {
	at := base.Add(time.Hour)
	closed := session(5, store.EndpointClosed, ctlName(4))
	for _, c := range []struct {
		rule string
		rec  store.OutLostRecord
	}{
		{"a record whose item is pending", lostNotice(5, 1, at)},
		{"a record whose item is gone and not yet swept", func() store.OutLostRecord { o := lostNotice(5, 1, at); o.Feedback = nil; return o }()},
	} {
		t.Run(c.rule, func(t *testing.T) {
			r := newRig(t)
			seed(t, r.mem, closed, c.rec)
			if plan := store.PlanSweep(r.scan(), outRetention(1000)); len(plan.Endpoints) != 0 {
				t.Errorf("the endpoint was planned: %v", plan.Endpoints)
			}
		})
	}
	t.Run("an out-lost record of another sender does not keep it", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, closed, lostNotice(6, 1, at))
		if plan := store.PlanSweep(r.scan(), outRetention(1000)); len(plan.Endpoints) != 1 {
			t.Errorf("endpoints planned: %v", plan.Endpoints)
		}
	})
	t.Run("the endpoint goes in the sweep after the one that removed the record", func(t *testing.T) {
		r := newRig(t)
		cleared := lostNotice(5, 1, at)
		cleared.Feedback = nil
		seed(t, r.mem, closed, cleared, store.SeqRecord{Sender: ep(5), Floors: map[ident.Address]uint64{addr(2, 2): 1}})
		sweepOnce(t, r, outRetention(1000))
		sc := r.scan()
		if len(sc.OutLost) != 0 || !hasEndpoint(sc, ep(5)) {
			t.Fatalf("after the first sweep: out-lost %v, endpoint kept %v", sc.OutLost, hasEndpoint(sc, ep(5)))
		}
		sweepOnce(t, r, outRetention(1000))
		if sc := r.scan(); hasEndpoint(sc, ep(5)) || len(sc.Seq) != 0 {
			t.Errorf("after the second sweep: endpoint %v, floors %v", hasEndpoint(sc, ep(5)), sc.Seq)
		}
	})
}

// An out-lost record whose sending endpoint is closed, or gone, is swept whatever
// its item says: closing is irreversible, so the notice can never be handed out
// and nothing would ever clear the item. The endpoint, which the record kept, goes
// in the sweep after it, as it does after a source record.
func TestPlanSweepTakesTheOutLostRecordOfAClosedEndpoint(t *testing.T) {
	at := base.Add(time.Hour)
	for _, c := range []struct {
		rule   string
		seeded []store.Record
		swept  bool
	}{
		{"the sender is closed", []store.Record{session(5, store.EndpointClosed, ctlName(4))}, true},
		{"the sender has no endpoint record", nil, true},
		{"the sender is open", []store.Record{session(5, store.EndpointOpen, ctlName(4))}, false},
		{"the sender is still being created", []store.Record{session(5, store.EndpointCreating, ctlName(4))}, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			r := newRig(t)
			pending := lostNotice(5, 1, at)
			seed(t, r.mem, append(c.seeded, pending)...)
			plan := store.PlanSweep(r.scan(), at)
			if got := len(plan.OutLosts) == 1; got != c.swept {
				t.Errorf("a record with its item pending was planned = %v, want %v: %v", got, c.swept, plan.OutLosts)
			}
		})
	}
	t.Run("the endpoint goes in the sweep after the one that removed the record", func(t *testing.T) {
		r := newRig(t)
		seed(t, r.mem, session(5, store.EndpointClosed, ctlName(4)), lostNotice(5, 1, at),
			store.SeqRecord{Sender: ep(5), Floors: map[ident.Address]uint64{addr(2, 2): 1}})
		if plan := sweepOnce(t, r, at); len(plan.OutLosts) != 1 || len(plan.Endpoints) != 0 {
			t.Fatalf("first sweep: out-lost %v, endpoints %v", plan.OutLosts, plan.Endpoints)
		}
		sc := r.scan()
		if len(sc.OutLost) != 0 || !hasEndpoint(sc, ep(5)) {
			t.Fatalf("after the first sweep: out-lost %v, endpoint kept %v", sc.OutLost, hasEndpoint(sc, ep(5)))
		}
		sweepOnce(t, r, at)
		if sc := r.scan(); hasEndpoint(sc, ep(5)) || len(sc.Seq) != 0 {
			t.Errorf("after the second sweep: endpoint %v, floors %v", hasEndpoint(sc, ep(5)), sc.Seq)
		}
	})
}

// A notice covers the item of an out-lost record by the name of the file and the
// kind, with no recipient (the item has none), and clears it with the same rule as
// for a source record: the record is written again without the item.
func TestANoticeClearsTheItemOfAnOutLostRecordItCovers(t *testing.T) {
	r := worldRig(t)
	a, b := lostNotice(1, 1, base), lostNotice(1, 2, base)
	notice := noticeForLost(a)
	seed(t, r.mem, a, b, notice)
	sc := r.scan()
	if len(sc.In) != 1 || len(sc.In[0].Covers) != 1 || !sc.In[0].Covers[0].Recipient.IsZero() {
		t.Fatalf("the cover did not survive the disk: %+v", sc.In)
	}
	lost := store.OutLostReplays(sc)
	if len(lost) != 1 || lost[0].Name() != a.Name() || len(lost[0].Feedback) != 0 || lost[0].DiscoveredAt != a.DiscoveredAt {
		t.Fatalf("OutLostReplays = %#v, want only the covered record, without its item", lost)
	}
	if got := store.FeedbackReplays(sc); len(got) != 0 {
		t.Errorf("the cover of an out-lost record is work of the replay of source records: %v", got)
	}
	mustDo(t, r.st.OutLostClear(lost))
	sc = r.scan()
	for _, o := range sc.OutLost {
		switch o.Name() {
		case a.Name():
			if len(o.Feedback) != 0 {
				t.Errorf("the covered item is still there: %+v", o)
			}
		case b.Name():
			if len(o.Feedback) != 1 {
				t.Errorf("the item of a record no notice covers was touched: %+v", o)
			}
		}
	}
	if again := store.OutLostReplays(sc); len(again) != 0 {
		t.Errorf("work after the clear: %v", again)
	}
	if err := r.st.OutLostClear(nil); err == nil {
		t.Error("a clear of nothing is a call that does nothing and says so")
	}
}

// A cover has a recipient exactly when the item it names has one.
func TestACoverHasARecipientExactlyWhenItsItemHasOne(t *testing.T) {
	lost := lostNotice(1, 1, base)
	for _, c := range []struct {
		rule  string
		cover store.CoverRef
		ok    bool
	}{
		{"an item of a delivery, with its recipient", store.CoverRef{Out: srcOut(4, false).Name(), Recipient: addr(2, 2), Kind: delivery.KindRejected}, true},
		{"an item of a delivery, without a recipient", store.CoverRef{Out: srcOut(4, false).Name(), Kind: delivery.KindRejected}, false},
		{"an item of a lost message, without a recipient", store.CoverRef{Out: lost.Name(), Kind: delivery.KindLostUnknownMessage}, true},
		{"an item of a lost message, with a recipient", store.CoverRef{Out: lost.Name(), Recipient: addr(2, 2), Kind: delivery.KindLostUnknownMessage}, false},
		{"a file that is not one of out/", store.CoverRef{Out: tgtIn(1).Name(), Kind: delivery.KindLostUnknownMessage}, false},
	} {
		t.Run(c.rule, func(t *testing.T) {
			n := noticeForLost(lost)
			n.Covers = []store.CoverRef{c.cover}
			_, err := store.EncodeRecord(n)
			if (err == nil) != c.ok {
				t.Errorf("EncodeRecord error %v, want ok = %v", err, c.ok)
			}
		})
	}
}
