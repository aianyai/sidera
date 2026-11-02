package engine

import (
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/store"
)

// Closing refuses what it must before it writes anything, so these cases need no
// store: an engine with a view is enough.
func TestCloseEndpointAnswersForWhatCannotBeClosed(t *testing.T) {
	runtime := endpointRec(t, 1, adapterreg.KindRuntime, store.EndpointOpen, "chat")
	closed := endpointRec(t, 2, adapterreg.KindDirect, store.EndpointClosed, "chat")
	v, err := buildView(nodeOf(t, 7), &store.Scan{Endpoints: []store.EndpointRecord{runtime, closed}})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{view: v}
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		id      ident.EndpointID
		outcome CloseOutcome
		reason  Reason
	}{
		{"a runtime endpoint stands for its instance", runtime.ID, 0, ReasonWrongEndpointKind},
		{"an endpoint that is closed already says so and writes nothing", closed.ID, CloseOutcomeAlreadyClosed, 0},
		{"an endpoint nobody has", idOf(t, 99), 0, ReasonEndpointUnknown},
		{"the system endpoint", ident.SystemEndpoint(), 0, ReasonEndpointUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, err := e.closeEndpoint(now, tc.id, nil)
			if tc.reason != 0 {
				if r, ok := ReasonOf(err); !ok || r != tc.reason {
					t.Errorf("error %v, want reason %s", err, tc.reason)
				}
				return
			}
			if err != nil || o != tc.outcome {
				t.Errorf("closeEndpoint = %v, %v; want %v", o, err, tc.outcome)
			}
		})
	}
}

func TestACloseSaysClosingOnlyForASessionTheNodeCreatedAndClosedForTheRest(t *testing.T) {
	attached := endpointRec(t, 4, adapterreg.KindSession, store.EndpointOpen, "chat")
	attached.Session = &store.SessionInfo{Origin: adapterreg.OriginAttached, NativeRef: "native-1"}
	for name, tc := range map[string]struct {
		rec  store.EndpointRecord
		want CloseOutcome
	}{
		"a session endpoint the node created": {endpointRec(t, 1, adapterreg.KindSession, store.EndpointOpen, "chat"), CloseOutcomeClosing},
		"an attached session endpoint":        {attached, CloseOutcomeClosed},
		"a direct endpoint":                   {endpointRec(t, 2, adapterreg.KindDirect, store.EndpointOpen, "chat"), CloseOutcomeClosed},
		"a runtime endpoint":                  {endpointRec(t, 3, adapterreg.KindRuntime, store.EndpointOpen, "chat"), CloseOutcomeClosed},
	} {
		if got := closeOutcomeOf(tc.rec); got != tc.want {
			t.Errorf("closeOutcomeOf(%s) = %v, want %v", name, got, tc.want)
		}
	}
	if CloseOutcomeClosing.String() != "closing" || CloseOutcomeClosed.String() != "closed" || CloseOutcomeAlreadyClosed.String() != "already_closed" {
		t.Error("the names of the outcomes")
	}
}
