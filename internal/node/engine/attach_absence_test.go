package engine_test

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

// The absence of the controller of an attached session. Each attachment is the
// controller's heartbeat; an endpoint whose controller has not attached for the
// endpoint's queue time (delivery.DefaultEndpointMaxQueue here), in the time the node
// was awake, and that holds nothing queued or leased, is closed. The harness runs the
// periodic task of the clock state every minute, as the daemon does, so the clock is
// moved a minute at a time.

const absenceWindow = delivery.DefaultEndpointMaxQueue

// attachedController is a node with one attached session whose controller attached
// once, at the start of the clock.
func attachedController(t *testing.T) (*harness.H, ident.EndpointID) {
	t.Helper()
	return attachedControllerOn(t, harness.Options{})
}

func attachedControllerOn(t *testing.T, o harness.Options) (*harness.H, ident.EndpointID) {
	t.Helper()
	h := sessionNode(t, o, "a")
	return h, attachController(t, h)
}

// attachController is what a controller's heartbeat does: it attaches the same
// session, and so finds the same endpoint.
func attachController(t *testing.T, h *harness.H) ident.EndpointID {
	t.Helper()
	res, err := h.Engine().AttachSession(h.Ctx(), engine.AttachRequest{Adapter: harness.Instance, NativeRef: "a-session-a-person-opened"})
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	return res.Endpoint.Endpoint
}

// awakeFor moves the clock a minute at a time for d, the node awake throughout.
func awakeFor(t *testing.T, h *harness.H, d time.Duration) {
	t.Helper()
	for ; d > 0; d -= time.Minute {
		h.Advance(time.Minute)
		quiet(t, h)
	}
}

func controllerOpen(t *testing.T, h *harness.H, id ident.EndpointID) bool {
	t.Helper()
	return endpointRecord(t, scanOf(t, h), id).Status == store.EndpointOpen
}

func wantControllerOpen(t *testing.T, h *harness.H, id ident.EndpointID, open bool, when string) {
	t.Helper()
	if got := controllerOpen(t, h, id); got != open {
		t.Fatalf("%s: the attached endpoint is open: %v, want %v", when, got, open)
	}
}

// A controller that never attaches again leaves its endpoint open for the queue time
// and no longer.
func TestAnAttachedEndpointClosesWhenItsControllerHasBeenGoneForTheQueueTime(t *testing.T) {
	h, id := attachedController(t)
	awakeFor(t, h, absenceWindow-time.Minute)
	wantControllerOpen(t, h, id, true, "a minute before the window ends")
	awakeFor(t, h, time.Minute)
	wantControllerOpen(t, h, id, false, "when the window ends")
}

// Each attachment starts the window again, so a controller that keeps attaching is
// never closed, however long it has been attached.
func TestAnAttachmentBeforeTheWindowEndsStartsItAgain(t *testing.T) {
	h, id := attachedController(t)
	awakeFor(t, h, absenceWindow-time.Minute)
	attachController(t, h)
	awakeFor(t, h, absenceWindow-time.Minute)
	wantControllerOpen(t, h, id, true, "a minute before the window of the second attachment ends")
	awakeFor(t, h, time.Minute)
	wantControllerOpen(t, h, id, false, "when the window of the second attachment ends")
}

// A queue time shorter than the controller's heartbeat allows is no shorter a window
// than three beats of it: a controller that is alive and whose beat is a little late
// keeps its endpoint, and one that has missed three is closed.
func TestAQueueTimeShorterThanThreeHeartbeatsDoesNotCloseALiveController(t *testing.T) {
	h, id := attachedControllerOn(t, harness.Options{NodeSettings: `{"endpoint_defaults":{"max_queue_s":60}}`})
	awakeFor(t, h, 2*time.Minute)
	wantControllerOpen(t, h, id, true, "two minutes after the attachment, the beat late")
	attachController(t, h)
	awakeFor(t, h, 2*time.Minute)
	wantControllerOpen(t, h, id, true, "two minutes after the late beat")
	awakeFor(t, h, time.Minute)
	wantControllerOpen(t, h, id, false, "three minutes after the last beat")
}

// A node that sleeps is not one the controller could attach to: a jump of the clock
// counts as no more than the longest step between two wakes of the loop, so the
// controller has the rest of the window after waking.
func TestTheTimeTheNodeSleptIsNotAbsenceOfTheController(t *testing.T) {
	h, id := attachedController(t)
	h.Advance(8 * time.Hour)
	quiet(t, h)
	wantControllerOpen(t, h, id, true, "after the node slept for hours")
	// What the sleep counted for, and a minute for each step after it: the window ends
	// on the twenty-ninth.
	awakeFor(t, h, 28*time.Minute)
	wantControllerOpen(t, h, id, true, "28 minutes after waking")
	awakeFor(t, h, time.Minute)
	wantControllerOpen(t, h, id, false, "29 minutes after waking")
}

// The start of the node counts as an attachment: a controller that went on running
// through the restart has the whole window to attach again.
func TestARestartGivesTheControllerAFreshWindow(t *testing.T) {
	h, id := attachedController(t)
	awakeFor(t, h, absenceWindow-5*time.Minute)
	h.Restart()
	quiet(t, h)
	awakeFor(t, h, absenceWindow-time.Minute)
	wantControllerOpen(t, h, id, true, "a minute before the window after the restart ends")
	awakeFor(t, h, time.Minute)
	wantControllerOpen(t, h, id, false, "when the window after the restart ends")
}

// A message queued for the endpoint, or leased on it, holds it open when the window
// ends: the message expires under the delivery rules, and the endpoint is closed after.
func TestAMessageOnTheEndpointHoldsItOpenPastTheWindow(t *testing.T) {
	for _, leased := range []bool{false, true} {
		name := "queued"
		if leased {
			name = "leased"
		}
		t.Run(name, func(t *testing.T) {
			h, id := attachedController(t)
			awakeFor(t, h, 20*time.Minute)
			sent, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{addressOf(id)}, Body: "for a session whose controller is gone"})
			if err != nil {
				t.Fatal(err)
			}
			msg := sent.MessageID
			quiet(t, h)
			if leased {
				awakeFor(t, h, 5*time.Minute)
				wantOnly(t, recvAs(t, h, h.MustSession(id.String()), fakeagent.RecvInput{}), msg)
			}
			// The queue time of the message ends 30 minutes after it was sent, 20 minutes
			// after the window of the controller.
			awakeFor(t, h, 20*time.Minute)
			if in := inOf(t, h, msg); in.Record.State != delivery.StateAccepted {
				t.Fatalf("the message is %s while the controller's window is over, want it still accepted", in.Record.State)
			}
			wantControllerOpen(t, h, id, true, "the window is over and the message is still there")
			awakeFor(t, h, 15*time.Minute)
			if in := inOf(t, h, msg); in.Record.State == delivery.StateAccepted {
				t.Fatalf("the message is still accepted after its queue time")
			}
			wantControllerOpen(t, h, id, false, "the message is gone")
		})
	}
}
