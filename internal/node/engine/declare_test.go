package engine_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

func ptr[T any](v T) *T { return &v }

func (r *rig) addr(b byte) ident.Address {
	return ident.Address{Node: r.node, Endpoint: mustEndpoint(r.t, b)}
}

func declared(t testing.TB, e *engine.Engine, id ident.EndpointID) authz.Declaration {
	t.Helper()
	eps, err := e.Endpoints(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range eps {
		if ep.ID == id {
			return ep.Declaration
		}
	}
	t.Fatalf("endpoint %s is not listed", id)
	return authz.Declaration{}
}

// A declaration is the endpoint's own statement about what it takes in, and a
// patch changes only what it names: the sender list is replaced as a whole.
func TestDeclareMergesThePatchAndKeepsItAcrossARestart(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	id := register(t, e, "a")
	caller := engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))

	if d := declared(t, e, id); d.Paused || d.RestrictSenders || len(d.AcceptFrom) != 0 {
		t.Fatalf("a new endpoint declares %+v, want nothing", d)
	}
	got, err := e.Declare(context.Background(), caller, engine.DeclarationPatch{
		Paused: ptr(true), RestrictSenders: ptr(true), AcceptFrom: &[]ident.Address{r.addr(5), r.addr(6)},
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	want := authz.Declaration{Paused: true, RestrictSenders: true, AcceptFrom: []ident.Address{r.addr(5), r.addr(6)}}
	if !declEqual(got, want) {
		t.Errorf("Declare returned %+v, want %+v", got, want)
	}

	got, err = e.Declare(context.Background(), caller, engine.DeclarationPatch{Paused: ptr(false), HiddenFromDirectory: ptr(true), AcceptFrom: &[]ident.Address{r.addr(7)}})
	if err != nil {
		t.Fatal(err)
	}
	want = authz.Declaration{RestrictSenders: true, HiddenFromDirectory: true, AcceptFrom: []ident.Address{r.addr(7)}}
	if !declEqual(got, want) {
		t.Errorf("second Declare returned %+v, want %+v: the patch names what changes and nothing else", got, want)
	}

	r.restart()
	e = r.start()
	if d := declared(t, e, id); !declEqual(d, want) {
		t.Errorf("after a restart the endpoint declares %+v, want %+v", d, want)
	}
	// An empty list says no sender is accepted, which is not the same as no list.
	got, err = e.Declare(context.Background(), engine.ByAdapter("chat", adapterproto.EndpointRef(id.String())), engine.DeclarationPatch{AcceptFrom: &[]ident.Address{}})
	if err != nil || len(got.AcceptFrom) != 0 || !got.RestrictSenders {
		t.Errorf("clearing the list: %+v, %v", got, err)
	}
}

func declEqual(a, b authz.Declaration) bool {
	if a.Paused != b.Paused || a.RefuseMultiRecipient != b.RefuseMultiRecipient || a.RestrictSenders != b.RestrictSenders ||
		a.HiddenFromDirectory != b.HiddenFromDirectory || len(a.AcceptFrom) != len(b.AcceptFrom) {
		return false
	}
	for i := range a.AcceptFrom {
		if a.AcceptFrom[i] != b.AcceptFrom[i] {
			return false
		}
	}
	return true
}

func TestDeclareRefusesWhatCannotBeStored(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	id := register(t, e, "a")
	caller := engine.ByAdapter("chat", adapterproto.EndpointRef(id.String()))

	tooMany := make([]ident.Address, engine.MaxAcceptFrom+1)
	for i := range tooMany {
		tooMany[i] = ident.Address{Node: r.node, Endpoint: mustEndpoint(t, byte(i%250+1))}
	}
	for _, tc := range []struct {
		name  string
		patch engine.DeclarationPatch
		field string
	}{
		{"an empty patch", engine.DeclarationPatch{}, "patch"},
		{"a zero address in the list", engine.DeclarationPatch{AcceptFrom: &[]ident.Address{{}}}, "accept_from"},
		{"more senders than an endpoint record may list", engine.DeclarationPatch{AcceptFrom: &tooMany}, "accept_from"},
		{"the same sender twice", engine.DeclarationPatch{AcceptFrom: &[]ident.Address{r.addr(5), r.addr(5)}}, "accept_from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.Declare(context.Background(), caller, tc.patch)
			ref := wantReason(t, err, engine.ReasonInvalidArgument)
			if ref.Field != tc.field {
				t.Errorf("field %q, want %q", ref.Field, tc.field)
			}
			if d := declared(t, e, id); d.Paused || len(d.AcceptFrom) != 0 {
				t.Errorf("a refused patch changed the declaration to %+v", d)
			}
		})
	}
}

// Who is calling is resolved in the command that does the work, and anything
// that is not the owner of an endpoint of the node fails closed.
func TestDeclareResolvesTheCallerAndRefusesEveryoneElse(t *testing.T) {
	noLeaks(t)
	r := newRig(t, 10)
	e := r.start()
	id := register(t, e, "a")
	ref := adapterproto.EndpointRef(id.String())
	patch := engine.DeclarationPatch{Paused: ptr(true)}

	closedID := register(t, e, "closed")
	if _, err := e.CloseEndpoint(context.Background(), engine.CloseRequest{Endpoint: closedID}); err != nil {
		t.Fatal(err)
	}
	settle(t, e)

	// The token of a direct endpoint is on its record, and opens nothing: only the
	// token of a session endpoint is a way in on the Agent interface.
	sc := r.scan()
	var directToken adapterproto.Token
	for _, ep := range sc.Endpoints {
		if ep.ID == id {
			directToken = adapterproto.Token(base64.RawURLEncoding.EncodeToString(ep.Token))
		}
	}
	if err := directToken.Validate(); err != nil {
		t.Fatalf("the token on the record is not a token: %v", err)
	}

	for _, tc := range []struct {
		name   string
		caller engine.Caller
		want   engine.Reason
	}{
		{"nobody", engine.Caller{}, engine.ReasonUnauthenticated},
		{"a token no endpoint holds", engine.ByToken(adapterproto.Token("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")), engine.ReasonUnauthenticated},
		{"the token of a direct endpoint", engine.ByToken(directToken), engine.ReasonUnauthenticated},
		{"another instance", engine.ByAdapter("other", ref), engine.ReasonEndpointUnknown},
		{"an endpoint nobody registered", engine.ByAdapter("chat", adapterproto.EndpointRef(mustEndpoint(t, 77).String())), engine.ReasonEndpointUnknown},
		{"a reference that is no endpoint", engine.ByAdapter("chat", adapterproto.EndpointRef("not an endpoint")), engine.ReasonEndpointUnknown},
		{"the system endpoint", engine.ByAdapter("chat", adapterproto.EndpointRef(ident.SystemEndpoint().String())), engine.ReasonEndpointUnknown},
		{"a closed endpoint", engine.ByAdapter("chat", adapterproto.EndpointRef(closedID.String())), engine.ReasonEndpointUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.Declare(context.Background(), tc.caller, patch)
			wantReason(t, err, tc.want)
		})
	}
	if d := declared(t, e, id); d.Paused {
		t.Errorf("a refused caller changed the declaration of the endpoint")
	}
}
