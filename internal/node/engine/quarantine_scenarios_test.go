package engine_test

import (
	"errors"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// A file of state/ that no longer checks out, or a body that is not what its record
// says, is a loss the node must not take silently. The file is settled by the
// store's quarantine, and what the engine owes is the sender's notice: the endpoint
// whose message was lost is told, once, in words that do not say the message was
// handed over, and the owner has an alert. These tests damage files of a node that
// is down, start it, and read what each party was told.

// corruptOutKind says how a file of out/ is damaged.
const (
	damaged = "damaged after a first line that can be trusted"
	zeroed  = "all zeros"
)

// scenario builds a node with the endpoints a and b and one message from a to b that
// the target took in and the source holds a receipt for.
func settledMessage(t *testing.T) (*harness.H, adapterproto.MessageID) {
	t.Helper()
	h := nodeWith(t, harness.Options{}, "a", "b")
	msg := send(t, h, "a", "b", "taken in and acknowledged")
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Fatalf("the source record is %s, want accepted", d.State)
	}
	return h, msg
}

// heldMessage is a message that the target took in and the source has no receipt for.
func heldMessage(t *testing.T) (*harness.H, adapterproto.MessageID) {
	t.Helper()
	h := nodeWith(t, harness.Options{}, "a", "b")
	h.Unreachable(true)
	msg := send(t, h, "a", "b", "taken in, its receipt held")
	quiet(t, h)
	h.HoldReports(string(msg))
	h.Unreachable(false)
	h.Advance(resendBackoff)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateQueued {
		t.Fatalf("the source record is %s, want queued: the report was not held", d.State)
	}
	return h, msg
}

func damage(t *testing.T, h *harness.H, how, dir, name string) {
	t.Helper()
	switch how {
	case damaged:
		damageBody(t, h, dir, name)
	case zeroed:
		zeroFile(t, h, dir, name)
	default:
		t.Fatalf("unknown damage %q", how)
	}
}

// settledOnce holds the node to what a loss owes after the node has been up for a
// while and restarted: the endpoint was told exactly the kinds given, the owner has
// the alerts, and neither a restart nor time says any of it again.
func settledOnce(t *testing.T, h *harness.H, told []delivery.Kind, alertKind string, alerts int) {
	t.Helper()
	h.Advance(mergeWindow)
	quiet(t, h)
	check := func(when string) {
		t.Helper()
		if got := toldKinds(t, h, "a"); !slices.Equal(got, told) {
			t.Errorf("%s: a was told %v, want %v", when, got, told)
		}
		if got := len(alertsOfKind(t, h, alertKind)); got != alerts {
			t.Errorf("%s: the owner has %d alerts of kind %s, want %d", when, got, alertKind, alerts)
		}
		if got := pending(t, h, "a"); got != 0 {
			t.Errorf("%s: %d items are still pending", when, got)
		}
	}
	check("at the start")
	h.Restart()
	quiet(t, h)
	h.Advance(time.Hour)
	quiet(t, h)
	check("after a restart and an hour")
}

// What the sender is told when the node loses a delivery record to damage depends on
// whose record it was and what the file still says.
func TestCorruptDeliveryRecordIsReportedToTheSenderOnce(t *testing.T) {
	t.Run("a source record whose first line can be trusted becomes the sender's lost record", func(t *testing.T) {
		h, msg := settledMessage(t)
		name := outOf(t, h, msg).Name()
		h.Stop()
		damage(t, h, damaged, store.OutDir, name)
		h.Start()
		quiet(t, h)
		o := outOf(t, h, msg)
		if o.Lost == nil || len(o.Lost.Deliveries) != 1 || o.Lost.Deliveries[0].State != delivery.StateDeliveryUnknown || o.Lost.Deliveries[0].Unknown != delivery.UnknownLostAtSender {
			t.Fatalf("the source record is %+v, want the sender's lost record: delivery_unknown, lost_at_sender", o)
		}
		h.Advance(mergeWindow)
		quiet(t, h)
		ns := noticesFor(t, h, "a")
		if len(ns) != 1 {
			t.Fatalf("a holds %d notices, want 1", len(ns))
		}
		for _, w := range []string{string(msg), addrOf(t, h, "b").String(), "this node lost what it had stored"} {
			if !strings.Contains(ns[0].text, w) {
				t.Errorf("the notice does not say %q:\n%s", w, ns[0].text)
			}
		}
		for _, w := range []string{"handed"} {
			if strings.Contains(ns[0].text, w) {
				t.Errorf("the notice says %q:\n%s", w, ns[0].text)
			}
		}
		settledOnce(t, h, []delivery.Kind{delivery.KindLostAtSender}, "corrupt_record", 1)
	})
	t.Run("a source record that is all zeros is told to its sender as a message nobody can name", func(t *testing.T) {
		h, msg := settledMessage(t)
		name := outOf(t, h, msg).Name()
		h.Stop()
		damage(t, h, zeroed, store.OutDir, name)
		h.Start()
		quiet(t, h)
		sc := scanOf(t, h)
		if len(sc.Out) != 0 || len(sc.OutLost) != 1 || sc.OutLost[0].Key.Sender != endpointOf(t, h, "a") {
			t.Fatalf("the disk holds %d source records and %d out-lost records, want the notice of the loss for a alone", len(sc.Out), len(sc.OutLost))
		}
		h.Advance(mergeWindow)
		quiet(t, h)
		ns := noticesFor(t, h, "a")
		if len(ns) != 1 || !strings.Contains(ns[0].text, "cannot be named") || strings.Contains(ns[0].text, "handed") {
			t.Fatalf("a was told %v: %+v", toldKinds(t, h, "a"), ns)
		}
		if got := scanOf(t, h).OutLost[0].Feedback; len(got) != 0 {
			t.Errorf("the out-lost record still holds %d items after the notice", len(got))
		}
		settledOnce(t, h, []delivery.Kind{delivery.KindLostUnknownMessage}, "corrupt_record", 1)
	})
	for _, how := range []string{damaged, zeroed} {
		t.Run("a target record "+how+" is removed and the source that holds a receipt is told by the next pull", func(t *testing.T) {
			h, msg := settledMessage(t)
			name := inOf(t, h, msg).Name()
			h.Stop()
			damage(t, h, how, store.InDir, name)
			h.Start()
			quiet(t, h)
			if ins := inRecords(t, h, msg); len(ins) != 0 {
				t.Fatalf("the target holds %d records of the message, want none: nothing replaces a target's lost record", len(ins))
			}
			h.Advance(pullInterval)
			quiet(t, h)
			if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown || d.Unknown != delivery.UnknownLostAtTarget {
				t.Fatalf("the source record is %s (%s), want delivery_unknown, lost_at_target", d.State, d.Unknown)
			}
			h.Advance(mergeWindow)
			quiet(t, h)
			ns := noticesFor(t, h, "a")
			if len(ns) != 1 {
				t.Fatalf("a holds %d notices, want 1", len(ns))
			}
			for _, w := range []string{"handed", "storage corruption"} {
				if strings.Contains(ns[0].text, w) {
					t.Errorf("the notice says %q:\n%s", w, ns[0].text)
				}
			}
			if ins := inRecords(t, h, msg); len(ins) != 0 {
				t.Errorf("the target holds %d records after the source was told: the message was sent again", len(ins))
			}
			settledOnce(t, h, []delivery.Kind{delivery.KindLostAtTarget}, "corrupt_record", 1)
		})
	}
	t.Run("a target record lost while the source holds no receipt is no loss at the target, and expires", func(t *testing.T) {
		h, msg := heldMessage(t)
		name := inOf(t, h, msg).Name()
		// The node is down past the deadline and the tolerance: it does not send the
		// message again (a send before the deadline would be taken in as new by a target
		// that lost its record, and is the one repeat the design accepts), it asks.
		h.Stop()
		zeroFile(t, h, store.InDir, name)
		h.Release(string(msg))
		h.Advance(delivery.DefaultAcceptBefore + 3*time.Minute)
		h.Start()
		quiet(t, h)
		if d := sourceDelivery(t, h, msg); d.State != delivery.StateExpiredUnconfirmed {
			t.Fatalf("the source record is %s (%s), want expired_unconfirmed", d.State, d.Unknown)
		}
		settledOnce(t, h, []delivery.Kind{delivery.KindExpiredUnconfirmed}, "corrupt_record", 1)
	})
}

// A body that is not what its record says is found when something reads it. Every
// record owns its body, so only that record is settled, by what it still needs: a
// source record that waits to send it, and a target record that waits to hand it
// out, are lost with it, each without the other, though the target took in the
// message from the source on the same node; a record that no longer needs the bytes
// keeps its state and drops the reference.
func TestBodyWithWrongHashIsQuarantinedAndRecordsTreatedAsLost(t *testing.T) {
	readBody := func(t *testing.T, h *harness.H, owner store.Record, ref store.BodyRef) error {
		t.Helper()
		var got error
		if err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) { got = l.ReadBody(owner, ref) }); err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("a source record that waits to send the body is lost with it, and the target record keeps the body of its own", func(t *testing.T) {
		h, msg := heldMessage(t)
		src, in := outOf(t, h, msg), inOf(t, h, msg)
		if src.Body == nil || in.Body == nil || store.BodyFileName(src.Name()) == store.BodyFileName(in.Name()) {
			t.Fatalf("the source record holds body %v and the target record %v, want a body of its own for each", src.Body, in.Body)
		}
		h.Stop()
		flipBodyFile(t, h, src)
		h.Start() // the source sends again, reads the body and finds it damaged
		quiet(t, h)

		o := outOf(t, h, msg)
		if o.Lost == nil || o.Lost.Deliveries[0].Unknown != delivery.UnknownLostAtSender {
			t.Fatalf("the source record is %+v, want the sender's lost record", o)
		}
		ins := inRecords(t, h, msg)
		if len(ins) != 1 || ins[0].Body == nil {
			t.Fatalf("the target holds %d records of the message with bodies %v, want its record as it was", len(ins), ins)
		}
		if _, err := h.Disk().ReadFile(path.Join(store.BodiesDir, store.BodyFileName(in.Name()))); err != nil {
			t.Errorf("the body of the target record: %v, want it left alone", err)
		}
		settledOnce(t, h, []delivery.Kind{delivery.KindLostAtSender}, "corrupt_body", 1)
	})
	t.Run("a target record that waits to hand out the body is lost with it, and the source record keeps the body of its own", func(t *testing.T) {
		h, msg := heldMessage(t)
		in := inOf(t, h, msg)
		h.Stop()
		flipBodyFile(t, h, in)
		h.Start()
		quiet(t, h)
		if err := readBody(t, h, in, *in.Body); !errors.Is(err, store.ErrBodyMismatch) {
			t.Fatalf("reading the body: %v, want it reported as not what its record says", err)
		}
		if ins := inRecords(t, h, msg); len(ins) != 0 {
			t.Fatalf("the target holds %d records of the message, want none", len(ins))
		}
		if o := outOf(t, h, msg); o.Lost != nil || o.Body == nil {
			t.Fatalf("the source record is %+v, want it as it was, with its body", o)
		}
		if n := len(alertsOfKind(t, h, "corrupt_body")); n != 1 {
			t.Errorf("the owner has %d alerts of kind corrupt_body, want 1", n)
		}
	})
	t.Run("a target record that needs the body is removed, and its source learns it from a pull", func(t *testing.T) {
		h, msg := settledMessage(t)
		in := inOf(t, h, msg)
		h.Stop()
		flipBodyFile(t, h, in)
		h.Start()
		quiet(t, h)
		if err := readBody(t, h, in, *in.Body); !errors.Is(err, store.ErrBodyMismatch) {
			t.Fatalf("reading the body: %v, want it reported as not what its record says", err)
		}
		if ins := inRecords(t, h, msg); len(ins) != 0 {
			t.Fatalf("the target holds %d records of the message, want none", len(ins))
		}
		ans, err := pullFor(h, msg, endpointOf(t, h, "b"))
		if err != nil || len(ans.NoRecord) != 1 {
			t.Fatalf("the target answers %+v (%v), want no record", ans, err)
		}
		h.Advance(pullInterval)
		quiet(t, h)
		if d := sourceDelivery(t, h, msg); d.State != delivery.StateDeliveryUnknown || d.Unknown != delivery.UnknownLostAtTarget {
			t.Fatalf("the source record is %s (%s), want delivery_unknown, lost_at_target", d.State, d.Unknown)
		}
		settledOnce(t, h, []delivery.Kind{delivery.KindLostAtTarget}, "corrupt_body", 1)
	})
	t.Run("a record that no longer needs the body keeps its state and drops the reference", func(t *testing.T) {
		// A rejected message: the source record is final and holds no body, and is not
		// written again at the start. A crash between the write that settled it and the
		// removal of the body can leave it referring to one.
		h := nodeWith(t, harness.Options{}, "a", "b")
		declineAll(t, h, "b")
		msg := send(t, h, "a", "b", "refused")
		quiet(t, h)
		o := outOf(t, h, msg)
		content := []byte("the body a crash left a record referring to")
		ref := store.BodyRefOf(content)
		o.Body = &ref
		data, err := store.EncodeRecord(o)
		if err != nil {
			t.Fatal(err)
		}
		h.Stop()
		writeOnDisk(t, h, store.OutDir, o.Name(), data)
		writeOnDisk(t, h, store.BodiesDir, store.BodyFileName(o.Name()), content)
		flipBodyFile(t, h, o)
		h.Start()
		quiet(t, h)
		if err := readBody(t, h, o, ref); !errors.Is(err, store.ErrBodyMismatch) {
			t.Fatalf("reading the body: %v", err)
		}
		got := outOf(t, h, msg)
		if got.Lost != nil || got.Body != nil || sourceDelivery(t, h, msg).State != delivery.StateRejected {
			t.Errorf("the source record is %+v, want the same delivery, rejected, with no body reference", got)
		}
		if n := len(alertsOfKind(t, h, "corrupt_body")); n != 1 {
			t.Errorf("the owner has %d alerts of kind corrupt_body, want 1", n)
		}
		h.Advance(mergeWindow)
		quiet(t, h)
		if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
			t.Errorf("a was told %v, want only the rejection it was owed: the settlement of a body it no longer needed tells nothing", got)
		}
	})
}

// A running node settles only a floor file that a write met damaged. A file of any
// other directory is left for the start, which settles it with the scan: the write
// that met one fails with a word in the log, and nothing is settled, told or alerted.
func TestADamagedFileOtherThanAFloorFileIsNotSettledWhileTheNodeRuns(t *testing.T) {
	h, msg := settledMessage(t)
	a := endpointOf(t, h, "a")
	name := a.String() + "." + strings.Repeat("ab", 32) + ".rec"
	if name == outOf(t, h, msg).Name() {
		t.Fatal("the name of the file made up is the name of a record")
	}
	writeOnDisk(t, h, store.OutDir, name, make([]byte, 64)) // behind the back of the process
	var settle error
	if err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) { settle = l.SettleDamaged(store.OutDir, name) }); err != nil || settle == nil {
		t.Fatalf("settling the file: %v, %v, want the settlement refused", err, settle)
	}
	if !slices.ContainsFunc(h.Log().Messages(), func(m string) bool { return strings.Contains(m, "only the start settles") }) {
		t.Errorf("the log %q says nothing of the file that was refused", h.Log().Messages())
	}
	h.Advance(mergeWindow)
	quiet(t, h)
	if sc := scanOf(t, h); len(sc.Damaged) != 1 || sc.Damaged[0].Name != name || len(sc.OutLost) != 0 {
		t.Errorf("the disk lists %d damaged files and %d out-lost records, want the file left as it was and no notice of its loss", len(sc.Damaged), len(sc.OutLost))
	}
	if got := len(alertsOfKind(t, h, "corrupt_record")); got != 0 {
		t.Errorf("the owner has %d alerts, want none for a file that was not settled", got)
	}
	if got := toldKinds(t, h, "a"); len(got) != 0 {
		t.Errorf("a was told %v of a file that was not settled", got)
	}
}

// A floor file that a write meets damaged is settled and the write goes on: the
// store refuses to read it as empty, and the engine does what the start would have
// done. The numbers of the sender are above everything it could have used.
func TestADamagedFloorFileMetWhileTheNodeRunsIsSettledAndTheWriteGoesOn(t *testing.T) {
	for _, how := range []string{damaged, zeroed} {
		t.Run(how, func(t *testing.T) {
			h := nodeWith(t, harness.Options{}, "a", "b")
			h.Unreachable(true)
			msg := send(t, h, "a", "b", "waits for a target that is not there")
			quiet(t, h)
			o := outOf(t, h, msg)
			first := o.Envelope.Seq[endpointOf(t, h, "b")]
			a := endpointOf(t, h, "a")
			floors := store.SeqRecord{Sender: a, Floors: map[ident.Address]uint64{addrOf(t, h, "b"): first}}
			data, err := store.EncodeRecord(floors)
			if err != nil {
				t.Fatal(err)
			}
			// The floor file is damaged behind the back of the running node, so that the
			// first write that reads it is the one that meets it: the record is lost with
			// its body, which needs the floors raised.
			writeOnDisk(t, h, store.SeqDir, floors.Name(), data)
			damage(t, h, how, store.SeqDir, floors.Name())
			var got error
			flipBodyFile(t, h, o)
			if err := engine.InLoop(h.Ctx(), h.Engine(), func(l engine.LoopSide) { got = l.ReadBody(o, *o.Body) }); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(got, store.ErrBodyMismatch) {
				t.Fatalf("reading the damaged body: %v", got)
			}
			if lost := outOf(t, h, msg); lost.Lost == nil {
				t.Fatalf("the source record is not lost: the write that raises the floors did not go on")
			}
			if n := len(alertsOfKind(t, h, "corrupt_record")); n < 1 {
				t.Errorf("the owner has %d alerts of the damaged floor file, want one", n)
			}
			h.Unreachable(false)
			next := send(t, h, "a", "b", "the next message of the pair")
			quiet(t, h)
			if n := outOf(t, h, next).Envelope.Seq[endpointOf(t, h, "b")]; n <= first+1 {
				t.Errorf("the next message of the pair is numbered %d, want above %d: the numbers the lost one could have used are given up", n, first+1)
			}
		})
	}
}

// flipBodyFile damages the body file of a record: its first byte changes, so that it
// is no longer what the record says.
func flipBodyFile(t *testing.T, h *harness.H, owner store.Record) {
	t.Helper()
	p := path.Join(store.BodiesDir, store.BodyFileName(owner.Name()))
	data, err := h.Disk().ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[0] ^= 0x01
	if err := fsx.Replace(h.Disk(), fsx.Tag{Tx: "offline", Role: "f"}, p, data, fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}
}

// Every step of the settlement of a damaged file is crashed at, for a source record,
// a file lost unread and a target record. Wherever the node died, the next start
// settles the same file again: one lost record or one notice of its loss, the sender
// told once, and one alert.
func TestQuarantineRecoversFromACrashAtEveryPoint(t *testing.T) {
	cov := &coverage{hit: map[fsx.Key]int{}}
	run := func(t *testing.T, dir func(*harness.H, adapterproto.MessageID) (string, string), how string, told delivery.Kind) {
		taker := newDiskTaker(cov, "quarantine")
		h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
		msg := send(t, h, "a", "b", "taken in and acknowledged")
		quiet(t, h)
		d, name := dir(h, msg)
		h.Stop()
		damage(t, h, how, d, name)
		taker.h = h
		h.Start()
		quiet(t, h)
		if len(taker.disks) == 0 {
			t.Fatal("no step of the settlement was reached")
		}
		for _, td := range taker.disks {
			t.Run(td.where, func(t *testing.T) {
				r := restartedAt(t, td.disk, td.at, "a", "b")
				if got := toldKinds(t, r, "a"); !slices.Equal(got, []delivery.Kind{told}) {
					t.Errorf("a was told %v, want %v once", got, told)
				}
				if got := len(alertsOfKind(t, r, "corrupt_record")); got != 1 {
					t.Errorf("the owner has %d alerts, want 1", got)
				}
				if got := pending(t, r, "a"); got != 0 {
					t.Errorf("%d items are still pending", got)
				}
			})
		}
	}
	t.Run("a source record whose first line can be trusted", func(t *testing.T) {
		run(t, func(h *harness.H, m adapterproto.MessageID) (string, string) {
			return store.OutDir, outOf(t, h, m).Name()
		}, damaged, delivery.KindLostAtSender)
	})
	t.Run("a source record that is all zeros", func(t *testing.T) {
		run(t, func(h *harness.H, m adapterproto.MessageID) (string, string) {
			return store.OutDir, outOf(t, h, m).Name()
		}, zeroed, delivery.KindLostUnknownMessage)
	})
	t.Run("a target record", func(t *testing.T) {
		run(t, func(h *harness.H, m adapterproto.MessageID) (string, string) {
			return store.InDir, inOf(t, h, m).Name()
		}, damaged, delivery.KindLostAtTarget)
	})
	for _, k := range harness.ProgramTable(t).Keys() {
		if k.Tag.Tx == "quarantine" && k.Tag.Role != "session" && cov.hit[k.Key] == 0 {
			t.Errorf("the step %s %q of transaction quarantine was never reached", k.Step, k.Tag.Role)
		}
	}
}
