package engineagent_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent/engineagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
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

// The words of an error have one home, apiwire, which tests them against a table
// written out from the interface specification; here every reason must come out
// of the fake agent as that mapping gives it.
func TestEveryEngineReasonMapsToACategoryAndCode(t *testing.T) {
	for _, r := range engine.Reasons() {
		refusal := &engine.Refusal{Reason: r}
		want := apiwire.FromError(refusal)
		err := engineagent.MapError(refusal)
		if got := fakeagent.CategoryOf(err); string(got) != string(want.Category) || !got.Valid() {
			t.Errorf("%s: category %q, want %q", r, got, want.Category)
		}
		if got := fakeagent.CodeOf(err); got != want.Code {
			t.Errorf("%s: code %q, want %q", r, got, want.Code)
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

func TestACallToANodeThatIsDownIsUnavailable(t *testing.T) {
	err := engineagent.MapError(fmt.Errorf("declare: %w", engineagent.ErrNodeNotRunning))
	if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryUnavailable {
		t.Errorf("category %q, want unavailable: the interface has no socket to talk to", got)
	}
	if got := fakeagent.CodeOf(err); got != "daemon_not_running" {
		t.Errorf("code %q, want daemon_not_running", got)
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
	}
	if len(calls) != 4 {
		t.Fatalf("%d calls listed: the agent has eleven methods and Declare, Send, Reply, Recv, Ack, Status and Control are the ones the engine offers", len(calls))
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
			// The ledger records the category and code, which a real server's answer
			// to an unknown method has too; what tells the two apart is the sentinel,
			// so that a test written before the method exists cannot pass on an
			// answer the server never gave.
			if !errors.Is(err, scenario.ErrNotAvailable) {
				t.Errorf("%s: %v is not scenario.ErrNotAvailable, so it reads as the server's own answer", name, err)
			}
		})
	}
}

func TestOtherErrorsAreNotTakenForNotSupported(t *testing.T) {
	for _, err := range []error{
		nil, errors.New("x"),
		&fakeagent.Error{Category: fakeagent.CategoryNotFound, Code: "message_unknown"},
		&fakeagent.Error{Category: fakeagent.CategoryInternal, Code: "method_unknown"},
		// The answer of a server that does not know the method is not a method the
		// engine layer lacks.
		&fakeagent.Error{Category: fakeagent.CategoryNotFound, Code: "method_unknown"},
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
