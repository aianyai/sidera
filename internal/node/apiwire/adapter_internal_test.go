package apiwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// adapterRow is what an adapter is told for a reason of the engine: the code, and
// the data object as JSON text, empty for none. Every number and word is written
// out from the table of error codes of the adapter protocol and from the interim
// rule for the refusals that have no code there yet, and not taken from the
// constants of the code under test.
type adapterRow struct {
	code int
	data string
}

var adapterRows = map[engine.Reason]adapterRow{
	engine.ReasonEndpointUnknown:   {1001, ""},
	engine.ReasonWrongEndpointKind: {1002, ""},
	engine.ReasonEndpointLimit:     {1007, ""},
	engine.ReasonInvalidArgument:   {-32602, ""},
	engine.ReasonShuttingDown:      {-32002, ""},

	engine.ReasonInstanceUnknown:   {-32603, ""},
	engine.ReasonUnauthenticated:   {-32603, ""},
	engine.ReasonOperationIDReused: {-32603, ""},
	engine.ReasonRecovering:        {-32603, ""},
	engine.ReasonStorageFatal:      {-32603, ""},

	engine.ReasonIdempotencyConflict: {-32603, ""},
	engine.ReasonMessageConflict:     {-32603, ""},
	engine.ReasonMessageUnknown:      {-32603, ""},
	engine.ReasonTooLarge:            {-32603, ""},
	engine.ReasonAlertUnknown:        {-32603, ""},
	engine.ReasonIdentityRetired:     {-32603, ""},

	engine.ReasonNodeQueueFull:      {-32603, `{"reason":"node_queue_full"}`},
	engine.ReasonStorageFull:        {-32603, `{"reason":"storage_full"}`},
	engine.ReasonStorageUnavailable: {-32603, `{"reason":"storage_unavailable"}`},
	engine.ReasonClockRollback:      {-32603, `{"reason":"clock_rollback"}`},
	engine.ReasonLoopBusy:           {-32603, `{"reason":"loop_busy"}`},
}

func asPeerError(t *testing.T, err error) *peer.Error {
	t.Helper()
	var pe *peer.Error
	if !errors.As(err, &pe) {
		t.Fatalf("the error is %T (%v), want a *peer.Error", err, err)
	}
	return pe
}

func TestEveryReasonOfTheEngineHasItsCodeForARequestOfAnAdapter(t *testing.T) {
	for _, r := range engine.Reasons() {
		row, ok := adapterRows[r]
		if !ok {
			t.Errorf("reason %s has no row in the expected table", r)
			continue
		}
		t.Run(r.String(), func(t *testing.T) {
			pe := asPeerError(t, adapterError(&engine.Refusal{Reason: r}))
			if pe.Code != row.code {
				t.Errorf("code %d, want %d", pe.Code, row.code)
			}
			if pe.Message == "" {
				t.Error("the error has no message")
			}
			if string(pe.Data) != row.data {
				t.Errorf("data %q, want %q", pe.Data, row.data)
			}
		})
	}
	if len(adapterRows) != len(engine.Reasons()) {
		t.Errorf("the table has %d rows for %d reasons", len(adapterRows), len(engine.Reasons()))
	}
}

// The limit that was hit is in the data, so that an adapter can tell its own
// declared limit from the node's; the field of a value that was not acceptable is
// in the message.
func TestARefusalCarriesTheLimitInItsDataAndTheFieldInItsMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  *engine.Refusal
		code int
		data string
		text string
	}{
		{"the limit of the instance", &engine.Refusal{Reason: engine.ReasonEndpointLimit, Limit: engine.LimitInstance}, 1007, `{"limit":"instance"}`, ""},
		{"the limit of the node", &engine.Refusal{Reason: engine.ReasonEndpointLimit, Limit: engine.LimitNode}, 1007, `{"limit":"node"}`, ""},
		{"a value", &engine.Refusal{Reason: engine.ReasonInvalidArgument, Field: "display_name"}, -32602, "", "display_name"},
		{"a value of no name", &engine.Refusal{Reason: engine.ReasonInvalidArgument}, -32602, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pe := asPeerError(t, adapterError(tc.ref))
			if pe.Code != tc.code || string(pe.Data) != tc.data {
				t.Errorf("code %d data %q, want %d %q", pe.Code, pe.Data, tc.code, tc.data)
			}
			if !strings.Contains(pe.Message, tc.text) || pe.Message == "" {
				t.Errorf("message %q does not carry %q", pe.Message, tc.text)
			}
		})
	}
}

// What the engine says about its own state of mind (a path, an identifier in the
// cause of a failure) stays off the wire: it is for the log.
func TestTheCauseOfARefusalIsNeverSentToTheAdapter(t *testing.T) {
	for _, r := range engine.Reasons() {
		pe := asPeerError(t, adapterError(&engine.Refusal{Reason: r, Field: "key", Err: errors.New("/home/owner/secret-path")}))
		raw, err := json.Marshal(pe)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "secret-path") {
			t.Errorf("%s: the cause reached the wire: %s", r, raw)
		}
	}
}

func TestAnErrorThatIsNotARefusalReachesTheHostUntouched(t *testing.T) {
	plain := errors.New("the disk said no")
	if got := adapterError(plain); got != plain {
		t.Errorf("a plain error became %v", got)
	}
	already := peer.NewError(1004, "unknown attempt")
	if got := adapterError(already); got != already {
		t.Errorf("an error that already is the wire's became %v", got)
	}
	if got := adapterError(nil); got != nil {
		t.Errorf("nil became %v", got)
	}
}

func TestARefusalThatTheEngineWrappedIsMappedAsTheRefusal(t *testing.T) {
	err := fmt.Errorf("registering: %w", &engine.Refusal{Reason: engine.ReasonEndpointUnknown})
	if pe := asPeerError(t, adapterError(err)); pe.Code != 1001 {
		t.Errorf("code %d, want 1001", pe.Code)
	}
}

// The protocol has no code for the refusals that mean "not now, nothing was
// recorded": 1007 is the limit of direct endpoints, -32001 promises that the
// handler did not run, and the other application codes name other things. Until it
// is decided they are answered as an internal error that names the reason.
func TestShouldStorageBackPressureOfAnAdapterRequestHaveACodeOfItsOwn(t *testing.T) {
	t.Skip("open question, no protocol change is made without a decision: a refusal for storage or capacity back pressure " +
		"(node_queue_full, storage_full, storage_unavailable, clock_rollback, loop_busy) is answered as -32603 with data " +
		`{"reason":"<name>"} until the owner of the protocol says which it is: a new application code, ` +
		"-32001 (which today promises that the handler did not run, true here but meant for the peer's own queue), " +
		"or 1007 (the direct-endpoint limit, which an adapter reads as a limit it can stop hitting)")
}
