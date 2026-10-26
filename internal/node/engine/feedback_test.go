package engine_test

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// Feedback is what the sender's node tells its endpoint when a delivery goes wrong
// (a refusal, a loss, an expiry): a notice of the system endpoint that the endpoint
// receives like a message. The tests below read the notices from the disk, where
// the node keeps them, and run on one node whose endpoints send to each other.

const (
	mergeWindow = delivery.DefaultFeedbackMergeWindow
	rateWindow  = delivery.DefaultFeedbackRateWindow
)

// A notice of the system endpoint goes to an endpoint whose queue is full: the
// anomaly it reports would otherwise go unheard, and there is no notice about a
// notice to tell the endpoint that it did. It takes no place of the queue, so the
// messages of other senders are refused for the room they use, not for the room the
// notices took.
func TestSystemFeedbackIsNotRefusedByFullBacklog(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b", "c")
	declineAll(t, h, "b")
	for i := 0; i < delivery.DefaultEndpointBacklogLimit; i++ {
		send(t, h, "c", "a", fmt.Sprintf("filling the queue of a, message %d", i))
	}
	quiet(t, h)

	msg := send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateRejected || d.Reject != delivery.RejectEndpointDeclined {
		t.Fatalf("the message is %s (%s), want rejected (endpoint_declined)", d.State, d.Reject)
	}
	h.Advance(mergeWindow)
	quiet(t, h)

	ns := noticesFor(t, h, "a")
	if len(ns) != 1 || !slices.Equal(ns[0].kinds(), []delivery.Kind{delivery.KindRejected}) {
		t.Fatalf("a holds %d notices (%v), want one that says its message was rejected", len(ns), toldKinds(t, h, "a"))
	}
	if in := ns[0].in; in.Record.State != delivery.StateAccepted || !in.Record.Exempt {
		t.Errorf("the notice is %s, exempt %v, want accepted exempt traffic", in.Record.State, in.Record.Exempt)
	}
	// The queue is still full for everyone else, and the notice is not part of it.
	late := send(t, h, "c", "a", "one more than the queue holds")
	quiet(t, h)
	if in := inOf(t, h, late); in.Record.State != delivery.StateRejected || in.Record.Reject != delivery.RejectBacklogFull {
		t.Errorf("the message after the notice is %s (%s), want rejected (backlog_full)", in.Record.State, in.Record.Reject)
	}
	queued := 0
	for _, in := range scanOf(t, h).In {
		if in.Key.Recipient == endpointOf(t, h, "a") && in.Record.OccupiesBacklogSlot() {
			queued++
		}
	}
	if queued != delivery.DefaultEndpointBacklogLimit {
		t.Errorf("%d records take a place of the queue of a, want %d: the notice took none", queued, delivery.DefaultEndpointBacklogLimit)
	}
}

// An anomaly is told to the sender once, in one notice of the system endpoint that
// covers the feedback the source record holds. The notice waits out the merge
// window, and the words say what the sender may conclude and about which message.
func TestAnAnomalyIsToldToTheSenderOnceInOneNotice(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	declineAll(t, h, "b")
	msg := send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	if got := pending(t, h, "a"); got != 1 {
		t.Fatalf("the sender holds %d pending items, want the one for the rejection", got)
	}

	h.Advance(mergeWindow - time.Second)
	quiet(t, h)
	if ns := noticesFor(t, h, "a"); len(ns) != 0 {
		t.Fatalf("a notice went out %v before its window was over", mergeWindow-time.Second)
	}
	h.Advance(time.Second)
	quiet(t, h)
	ns := noticesFor(t, h, "a")
	if len(ns) != 1 {
		t.Fatalf("a holds %d notices once the window is over, want 1", len(ns))
	}
	n := ns[0]
	want := []string{delivery.KindRejected.Summary(), string(msg), "endpoint_declined", addrOf(t, h, "b").String()}
	for _, w := range want {
		if !strings.Contains(n.text, w) {
			t.Errorf("the notice does not say %q:\n%s", w, n.text)
		}
	}
	if n.in.Handout.ContentType != "text/plain" || !n.in.Key.Sender.IsSystem() || n.in.Key.SenderNode != harness.NodeID() {
		t.Errorf("the notice is %q from %v of %v, want text/plain from the system endpoint of this node", n.in.Handout.ContentType, n.in.Key.Sender, n.in.Key.SenderNode)
	}
	if got := pending(t, h, "a"); got != 0 {
		t.Errorf("%d items are pending after the notice covered them", got)
	}
	if len(n.in.Covers) != 1 || n.in.Covers[0].Out != outOf(t, h, msg).Name() || n.in.Covers[0].Recipient != addrOf(t, h, "b") {
		t.Errorf("the notice covers %+v, want the item of the source record of the message for b", n.in.Covers)
	}

	// Neither time nor a restart tells it again.
	h.Advance(time.Hour)
	quiet(t, h)
	h.Restart()
	quiet(t, h)
	h.Advance(time.Hour)
	quiet(t, h)
	if got := noticesFor(t, h, "a"); len(got) != 1 {
		t.Errorf("a holds %d notices an hour and a restart later, want 1", len(got))
	}
}

// An endpoint is told at most RateCap notices in a rate window; the rest wait and
// are sent when the window lets them, never dropped, and a restart does not give
// the endpoint a new window.
func TestNoticesOverTheRateCapAreHeldAndNeverDropped(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	declineAll(t, h, "b")
	var sent []adapterproto.MessageID
	for i := 0; i <= delivery.DefaultFeedbackRateCap; i++ {
		sent = append(sent, send(t, h, "a", "b", fmt.Sprintf("refused message %d", i)))
	}
	quiet(t, h)

	h.Advance(mergeWindow)
	quiet(t, h)
	if got := len(noticesFor(t, h, "a")); got != delivery.DefaultFeedbackRateCap {
		t.Fatalf("a holds %d notices after the first window, want the cap of %d", got, delivery.DefaultFeedbackRateCap)
	}
	if got := pending(t, h, "a"); got != 1 {
		t.Fatalf("%d items are pending, want the one that is held", got)
	}

	h.Restart()
	quiet(t, h)
	h.Advance(rateWindow - 2*mergeWindow)
	quiet(t, h)
	if got := len(noticesFor(t, h, "a")); got != delivery.DefaultFeedbackRateCap {
		t.Fatalf("a holds %d notices before the rate window is over, a restart in between, want %d: a restart gave it a new window", got, delivery.DefaultFeedbackRateCap)
	}

	h.Advance(2 * mergeWindow)
	quiet(t, h)
	if got := len(noticesFor(t, h, "a")); got != delivery.DefaultFeedbackRateCap+1 {
		t.Fatalf("a holds %d notices once the rate window is over, want all %d", got, delivery.DefaultFeedbackRateCap+1)
	}
	told := map[string]int{}
	for _, n := range noticesFor(t, h, "a") {
		for _, w := range sent {
			if strings.Contains(n.text, string(w)) {
				told[string(w)]++
			}
		}
	}
	for _, w := range sent {
		if told[string(w)] != 1 {
			t.Errorf("message %s is told %d times, want once", w, told[string(w)])
		}
	}
}

// Feedback that fell due while the node was down goes out at the start, in the
// step that sends what is due.
func TestFeedbackThatFellDueWhileTheNodeWasDownIsSentAtTheStart(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	declineAll(t, h, "b")
	send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	h.Stop()
	h.Advance(2 * mergeWindow)
	h.Start()
	quiet(t, h)
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
		t.Errorf("a was told %v at the start, want the rejection", got)
	}
}

// The endpoint a notice is for was closed before the notice went out. Closing ends
// the endpoint's life for feedback: an endpoint opened again with the same id does
// not hear of what happened before it was closed, as the records queued for a closing
// endpoint are not carried over to it either. So the feedback is dropped with a word
// in the log, and does not stay on the source record for ever.
func TestFeedbackForAnEndpointThatWasClosedIsDroppedAndLogged(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	declineAll(t, h, "b")
	send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: endpointOf(t, h, "a")}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Advance(mergeWindow)
	quiet(t, h)

	if ns := noticesFor(t, h, "a"); len(ns) != 0 {
		t.Errorf("a closed endpoint was given %d notices", len(ns))
	}
	if got := pending(t, h, "a"); got != 0 {
		t.Errorf("%d items stay on the records of a closed endpoint", got)
	}
	if !slices.ContainsFunc(h.Log().Messages(), func(m string) bool { return strings.Contains(m, "dropped") }) {
		t.Errorf("the log %q says nothing of the feedback that was dropped", h.Log().Messages())
	}
}

// Every step of the writes that tell an endpoint, the notice and the clearing of
// what it covers, is crashed at. Wherever the node died, the endpoint ends up told
// once: one notice, and nothing left pending.
func TestFeedbackRecoversFromACrashAtEveryPoint(t *testing.T) {
	cov := &coverage{hit: map[fsx.Key]int{}}
	taker := newDiskTaker(cov, "feedback-form", "feedback-clear")
	h := nodeWith(t, harness.Options{Observer: taker}, "a", "b")
	taker.h = h
	declineAll(t, h, "b")
	msg := send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	h.Advance(mergeWindow)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); len(got) != 1 {
		t.Fatalf("a was told %v without a crash, want one notice", got)
	}
	if len(taker.disks) == 0 {
		t.Fatal("no step of the watched transactions was reached")
	}
	for _, td := range taker.disks {
		t.Run(td.where, func(t *testing.T) {
			r := restartedAt(t, td.disk, td.at, "a", "b")
			if got := toldKinds(t, r, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
				t.Errorf("a was told %v, want the rejection once", got)
			}
			if got := pending(t, r, "a"); got != 0 {
				t.Errorf("%d items are still pending", got)
			}
			if d := sourceDelivery(t, r, msg); d.State != delivery.StateRejected {
				t.Errorf("the source record is %s, want rejected", d.State)
			}
		})
	}
	for _, k := range harness.ProgramTable(t).Keys() {
		if (k.Tag.Tx == "feedback-form" || k.Tag.Tx == "feedback-clear") && cov.hit[k.Key] == 0 && !leftoverBodyStep(k.Tag) {
			t.Errorf("the step %s %q of transaction %s was never reached", k.Step, k.Tag.Role, k.Tag.Tx)
		}
	}
}

// leftoverBodyStep reports whether the step is the removal of a body by the clearing
// of what a notice covers. The store removes a body when the write that leaves its
// record without it has been made, so by the time feedback is cleared the body is
// gone; the step only has a file to remove after a crash between those two writes,
// which the crash walk of the store covers.
func leftoverBodyStep(tag fsx.Tag) bool {
	return tag.Tx == "feedback-clear" && (tag.Role == "body" || tag.Role == "body-dir")
}

// A notice that could not be written tells nobody and loses nothing: the feedback
// stays queued, the node goes on, and the notice is written, once, when the write is
// tried again.
func TestANoticeThatCouldNotBeWrittenIsTriedAgainAndToldOnce(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	declineAll(t, h, "b")
	send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	rule := h.Faults().Fail(fsxtest.Match{Tx: "feedback-form", Role: "body", Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
	h.Advance(mergeWindow)
	quiet(t, h)
	if rule.Hits() == 0 {
		t.Fatal("the fault was not injected")
	}
	if got := toldKinds(t, h, "a"); len(got) != 0 {
		t.Fatalf("a was told %v by a notice that was not written", got)
	}
	if got := pending(t, h, "a"); got != 1 {
		t.Fatalf("%d items are pending after the failed write, want the one that was not told", got)
	}
	if got := h.Engine().Phase(); got != engine.PhaseRunning {
		t.Fatalf("the engine is %v after a write that failed before its commit point, want it running", got)
	}
	h.Advance(engine.DefaultParams().RetryBackoff)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
		t.Errorf("a was told %v after the write was tried again, want the rejection once", got)
	}
}

// The notice is written and then the items are taken off their records. When the
// second write fails, a retry may cure it, the endpoint has been told and is not told
// again, and the clearing is tried again after the back-off rather than left to the
// next start: the notice may be swept by then, and with it the covers that say the
// items were told, so that a restart would tell the endpoint a second time.
func TestANoticeIsNotToldTwiceWhenTheRecordsCouldNotBeCleared(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	declineAll(t, h, "b")
	send(t, h, "a", "b", "to an endpoint that takes nothing")
	quiet(t, h)
	rule := h.Faults().Fail(fsxtest.Match{Tx: "feedback-clear", Step: fsx.StepWrite, Hit: 1}, fsxtest.DiskFull)
	h.Advance(mergeWindow)
	quiet(t, h)
	if rule.Hits() == 0 {
		t.Fatal("the fault was not injected")
	}
	if got := toldKinds(t, h, "a"); !slices.Equal(got, []delivery.Kind{delivery.KindRejected}) {
		t.Fatalf("a was told %v, want the rejection: the notice was written", got)
	}
	if got := pending(t, h, "a"); got != 1 {
		t.Fatalf("%d items are pending, want the one the failed write left on its record", got)
	}
	h.Advance(engine.DefaultParams().RetryBackoff)
	quiet(t, h)
	if got := pending(t, h, "a"); got != 0 {
		t.Fatalf("%d items are pending after the clearing was tried again, want none", got)
	}
	if got := len(noticesFor(t, h, "a")); got != 1 {
		t.Fatalf("a holds %d notices after the clearing was tried again, want 1", got)
	}
	// The notice goes: nothing is left that says the items were told, except that
	// they are no longer on their records.
	notices := noticesFor(t, h, "a")
	h.Stop()
	for _, n := range notices {
		for _, f := range []string{path.Join(store.InDir, n.in.Name()), path.Join(store.BodiesDir, store.BodyFileName(n.in.Name()))} {
			if err := h.Disk().Remove(fsx.Tag{Tx: "offline", Role: "f"}, f); err != nil {
				t.Fatalf("removing %s: %v", f, err)
			}
		}
	}
	h.Start()
	quiet(t, h)
	h.Advance(time.Hour)
	quiet(t, h)
	if got := len(noticesFor(t, h, "a")); got != 0 {
		t.Errorf("a holds %d notices after the first was swept and the node restarted, want none: it was told once", got)
	}
}
