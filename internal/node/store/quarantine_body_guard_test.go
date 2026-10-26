package store_test

import (
	"errors"
	"testing"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/store"
)

// A target record has no replacement, so one that still needs its body cannot
// lose the reference to it: the delivery would wait to be handed out with nothing
// to hand. A quarantine of a body refuses such a record before it writes anything.
// A record that no longer needs the body is written without the reference.
func TestABodyQuarantineRefusesATargetRecordThatStillNeedsTheBodyItDrops(t *testing.T) {
	needing := inRev(1, 1)
	needing.Body = nil
	if !needing.Record.NeedsBody() {
		t.Fatalf("the record under test must still need its body: %+v", needing.Record)
	}
	finished := inRev(2, 1)
	finished.Record, finished.Body = deliveredRecord(finished.Record), nil
	if finished.Record.NeedsBody() {
		t.Fatalf("the finished record must not need its body: %+v", finished.Record)
	}
	quarantine := func(ins ...store.InRecord) store.BodyQuarantine {
		return store.BodyQuarantine{Name: store.BodyRefOf(bodyBytes(1)).Name(), AlertID: alertFor(store.BodiesDir, "x"), Rename: true, In: ins}
	}

	t.Run("refused, with nothing written and no crash point reached", func(t *testing.T) {
		r := newRig(t)
		seedBodies(t, r.mem, bodyBytes(1))
		before := snapshot(t, r.mem, ".")
		r.rec.forget()
		// The record that may go is refused with the one that may not.
		_, err := r.st.QuarantineBody(quarantine(finished, needing))
		if !errors.Is(err, store.ErrBodyStillNeeded) {
			t.Fatalf("err = %v, want ErrBodyStillNeeded", err)
		}
		if n := r.rec.count(); n != 0 {
			t.Errorf("%d crash points were reached by a call that was refused", n)
		}
		after := snapshot(t, r.mem, ".")
		if len(after) != len(before) {
			t.Errorf("the disk changed: %v -> %v", keysOf(before), keysOf(after))
		}
		for k, v := range before {
			if after[k] != v {
				t.Errorf("%s changed on disk", k)
			}
		}
	})

	t.Run("a record that no longer needs the body loses the reference", func(t *testing.T) {
		r := newRig(t)
		seedBodies(t, r.mem, bodyBytes(1))
		written, err := r.st.QuarantineBody(quarantine(finished))
		if err != nil {
			t.Fatal(err)
		}
		if len(written) != 1 || written[0].Body != nil || written[0].Record.State != delivery.StateDelivered {
			t.Errorf("written = %+v", written)
		}
	})
}
