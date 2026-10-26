package apiwire

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// adapterRow is what an adapter is told for a reason of the engine: the code, and
// the data object as JSON text, empty for none. Every number and word is written
// out from the table of error codes of the adapter protocol, and not taken from
// the constants of the code under test.
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
	engine.ReasonStorageFatal:      {-32603, ""},

	engine.ReasonIdempotencyConflict: {-32603, ""},
	engine.ReasonMessageConflict:     {-32603, ""},
	engine.ReasonMessageUnknown:      {-32603, ""},
	engine.ReasonTooLarge:            {-32603, ""},
	engine.ReasonAlertUnknown:        {-32603, ""},
	engine.ReasonIdentityRetired:     {-32603, ""},

	engine.ReasonInstallInProgress:   {-32603, ""},
	engine.ReasonInstallRepeated:     {-32603, ""},
	engine.ReasonPlatformUnsupported: {-32603, ""},
	engine.ReasonProbeTokenUsed:      {-32603, ""},
	engine.ReasonProbeTokenExpired:   {-32603, ""},

	engine.ReasonNodeQueueFull:      {1012, `{"reason":"node_queue_full"}`},
	engine.ReasonStorageFull:        {1012, `{"reason":"storage_full"}`},
	engine.ReasonStorageUnavailable: {1012, `{"reason":"storage_unavailable"}`},
	engine.ReasonClockRollback:      {1012, `{"reason":"clock_rollback"}`},
	engine.ReasonLoopBusy:           {1012, `{"reason":"loop_busy"}`},
	engine.ReasonRecovering:         {1012, `{"reason":"recovering"}`},
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

// A refusal that means "not now, nothing was recorded" is node_busy, 1012, with the
// reason in its data: the node refused before any effect and a later retry is
// safe. The error is one the protocol's own rules accept, the peer's effect rule
// reads it as no effect, and exactly the reasons the protocol lists as the ones
// this version sends are the ones that map to it: the one place the sender is
// held to that list. A failure whose outcome is not known is no back pressure
// and stays an internal error.
func TestBackPressureOfAnAdapterRequestIsNodeBusyWithItsReason(t *testing.T) {
	busy := map[string]bool{}
	for _, r := range engine.Reasons() {
		pe := asPeerError(t, adapterError(&engine.Refusal{Reason: r}))
		if pe.Code != 1012 {
			continue
		}
		busy[r.String()] = true
		t.Run(r.String(), func(t *testing.T) {
			if want := `{"reason":"` + r.String() + `"}`; string(pe.Data) != want {
				t.Errorf("data %q, want %q", pe.Data, want)
			}
			if err := adapterproto.ErrorObject(*pe).Validate(); err != nil {
				t.Errorf("the protocol refuses the error it is answered with: %v", err)
			}
			if !peer.NoEffect(pe) {
				t.Error("the peer's effect rule does not read the error as no effect")
			}
		})
	}
	want := map[string]bool{}
	for _, name := range adapterproto.BusyReasons() {
		want[name] = true
	}
	if !reflect.DeepEqual(busy, want) {
		t.Errorf("the reasons answered with 1012 are %v, the protocol names %v", busy, want)
	}

	pe := asPeerError(t, adapterError(&engine.Refusal{Reason: engine.ReasonStorageFatal}))
	if pe.Code != peer.CodeInternalError || pe.Data != nil || !peer.ResultUnknown(pe) {
		t.Errorf("a fatal storage error is code %d data %q, want an internal error without data whose outcome is unknown", pe.Code, pe.Data)
	}
}
