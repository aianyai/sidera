package engineagent_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
)

func ptr[T any](v T) *T { return &v }

func TestDeclarePatchReachesTheEngineForTheCallersOwnEndpoint(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.MustDirect("b")
	ctx := context.Background()
	err := h.Agent("a").Declare(ctx, fakeagent.DeclareInput{
		Paused: ptr(true), RestrictSenders: ptr(true), AcceptFrom: ptr([]string{h.Addr("b")}),
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	eps, err := h.Engine().Endpoints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		d := ep.Declaration
		switch ep.Address.String() {
		case h.Addr("a"):
			if !d.Paused || !d.RestrictSenders || len(d.AcceptFrom) != 1 || d.AcceptFrom[0].String() != h.Addr("b") {
				t.Errorf("endpoint a declares %+v, want paused, restricted, accepting b", d)
			}
		case h.Addr("b"):
			if d.Paused || d.RestrictSenders || len(d.AcceptFrom) != 0 {
				t.Errorf("endpoint b declares %+v: a patch for a changed it", d)
			}
		}
	}
}

func TestDeclareLeavesAFieldThatIsNilAsItWas(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	ctx := context.Background()
	if err := h.Agent("a").Declare(ctx, fakeagent.DeclareInput{Paused: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if err := h.Agent("a").Declare(ctx, fakeagent.DeclareInput{HiddenFromDirectory: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	eps, _ := h.Engine().Endpoints(ctx)
	if d := eps[0].Declaration; !d.Paused || !d.HiddenFromDirectory {
		t.Errorf("declaration %+v: the second patch undid the first", d)
	}
}

func TestDeclareRefusalsAreMappedToTheCategoryAndCodeOfTheInterface(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		in       fakeagent.DeclareInput
		category fakeagent.Category
		code     string
	}{
		{"an empty patch", fakeagent.DeclareInput{}, fakeagent.CategoryInvalidArgument, "empty_patch"},
		{"an address that does not parse", fakeagent.DeclareInput{AcceptFrom: ptr([]string{"not-an-address"})}, fakeagent.CategoryInvalidArgument, "malformed_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.Agent("a").Declare(ctx, tc.in)
			if got := fakeagent.CategoryOf(err); got != tc.category {
				t.Errorf("category %q, want %q (error %v)", got, tc.category, err)
			}
			if got := fakeagent.CodeOf(err); got != tc.code {
				t.Errorf("code %q, want %q (error %v)", got, tc.code, err)
			}
		})
	}
}

// The expectations are written out from the table of categories of the agent
// interface, not computed from the mapping under test.
func TestEveryEngineReasonMapsToACategoryAndCode(t *testing.T) {
	want := map[engine.Reason]struct {
		category fakeagent.Category
		code     string
	}{
		engine.ReasonUnauthenticated:    {fakeagent.CategoryUnauthenticated, "token_unknown"},
		engine.ReasonEndpointUnknown:    {fakeagent.CategoryNotFound, "endpoint_unknown"},
		engine.ReasonInstanceUnknown:    {fakeagent.CategoryNotFound, "instance_unknown"},
		engine.ReasonWrongEndpointKind:  {fakeagent.CategoryRefused, "endpoint_kind"},
		engine.ReasonEndpointLimit:      {fakeagent.CategoryRefused, "endpoint_limit"},
		engine.ReasonInvalidArgument:    {fakeagent.CategoryInvalidArgument, "malformed_request"},
		engine.ReasonOperationIDReused:  {fakeagent.CategoryConflict, "operation_id_reused"},
		engine.ReasonNodeQueueFull:      {fakeagent.CategoryBackpressure, "node_queue_full"},
		engine.ReasonStorageFull:        {fakeagent.CategoryBackpressure, "storage_full"},
		engine.ReasonStorageUnavailable: {fakeagent.CategoryBackpressure, "storage_unavailable"},
		engine.ReasonClockRollback:      {fakeagent.CategoryBackpressure, "clock_rollback"},
		engine.ReasonLoopBusy:           {fakeagent.CategoryBackpressure, "loop_busy"},
		engine.ReasonRecovering:         {fakeagent.CategoryUnavailable, "recovering"},
		engine.ReasonShuttingDown:       {fakeagent.CategoryUnavailable, "shutting_down"},
		engine.ReasonStorageFatal:       {fakeagent.CategoryInternal, "storage_fatal"},
	}
	for _, r := range engine.Reasons() {
		w, ok := want[r]
		if !ok {
			t.Errorf("reason %s has no expectation in this test: a reason was added without its mapping", r)
			continue
		}
		err := engineagent.MapError(&engine.Refusal{Reason: r})
		if got := fakeagent.CategoryOf(err); got != w.category {
			t.Errorf("%s: category %q, want %q", r, got, w.category)
		}
		if got := fakeagent.CodeOf(err); got != w.code {
			t.Errorf("%s: code %q, want %q", r, got, w.code)
		}
	}
}

func TestAnErrorThatIsNotARefusalIsInternal(t *testing.T) {
	err := engineagent.MapError(errors.New("disk on fire"))
	if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryInternal {
		t.Errorf("category %q, want internal", got)
	}
	if engineagent.MapError(nil) != nil {
		t.Error("MapError(nil) is not nil")
	}
}

func TestACallWhoseProcessEndedIsOutcomeUnknown(t *testing.T) {
	err := engineagent.MapError(fmt.Errorf("declare: %w", engineagent.ErrProcessEnded))
	if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryOutcomeUnknown {
		t.Errorf("category %q, want outcome_unknown: the request was taken and no answer could be read", got)
	}
	if got := fakeagent.CodeOf(err); got != "connection_lost" {
		t.Errorf("code %q, want connection_lost", got)
	}
}

func TestCallsTheEngineDoesNotOfferYetReturnTheNotSupportedError(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	a := h.Agent("a")
	ctx := context.Background()
	calls := map[string]func() error{
		"Me":        func() error { _, err := a.Me(ctx); return err },
		"Peers":     func() error { _, err := a.Peers(ctx); return err },
		"Directory": func() error { _, err := a.Directory(ctx, fakeagent.DirectoryInput{}); return err },
		"Probe":     func() error { _, err := a.Probe(ctx, fakeagent.ProbeInput{}); return err },
		"Send": func() error {
			_, err := a.Send(ctx, fakeagent.SendInput{To: []string{h.Addr("a")}, Body: "x"})
			return err
		},
		"Reply":   func() error { _, err := a.Reply(ctx, fakeagent.ReplyInput{}); return err },
		"Recv":    func() error { _, err := a.Recv(ctx, fakeagent.RecvInput{}); return err },
		"Ack":     func() error { _, err := a.Ack(ctx, fakeagent.AckInput{}); return err },
		"Control": func() error { _, err := a.Control(ctx, fakeagent.ControlInput{}); return err },
		"Status":  func() error { _, err := a.Status(ctx, fakeagent.StatusInput{}); return err },
	}
	if len(calls) != 10 {
		t.Fatalf("%d calls listed: the agent has eleven methods and Declare is the one the engine offers", len(calls))
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !engineagent.IsNotSupported(err) {
				t.Fatalf("%s returned %v, want the not-supported error", name, err)
			}
			if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryNotFound {
				t.Errorf("category %q: the agent interface answers a method it does not have with not_found", got)
			}
			if got := fakeagent.CodeOf(err); got != "method_unknown" {
				t.Errorf("code %q, want method_unknown", got)
			}
		})
	}
}

func TestOtherErrorsAreNotTakenForNotSupported(t *testing.T) {
	for _, err := range []error{
		nil, errors.New("x"),
		&fakeagent.Error{Category: fakeagent.CategoryNotFound, Code: "message_unknown"},
		&fakeagent.Error{Category: fakeagent.CategoryInternal, Code: "method_unknown"},
	} {
		if engineagent.IsNotSupported(err) {
			t.Errorf("%v is taken for not supported", err)
		}
	}
}

// The agent interface has no category for "the server does not do this yet"; a
// method the engine has not learned is answered as a method the server does not
// know. Whether a dedicated category or code would serve scripts better is a
// question for the owner of the interface's table, not for this backend.
func TestShouldAMethodTheEngineDoesNotOfferYetHaveItsOwnCategory(t *testing.T) {
	t.Skip("question for the owner of the error categories: the fake agent has no 'not supported' category, so the backend answers not_found/method_unknown")
}
