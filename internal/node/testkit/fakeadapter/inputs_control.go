package fakeadapter

import (
	"context"
	"encoding/json"

	"github.com/aianyai/sidera/internal/node/testkit/fakectl"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/peer"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// Private adapter control extensions; the generic control protocol is unchanged.
const (
	MethodInputsAttach = "adapter.inputs.attach"
	KnobExternalInputs = "external-inputs"
)

// HandleControl installs the adapter-owned readiness handshake. The reverse
// installation and its acknowledgement finish before the adapter may Serve.
func (r *InputRegistry) HandleControl(server *fakectl.Server) {
	server.Handle(MethodInputsAttach, func(ctx context.Context, from *fakectl.Conn, raw json.RawMessage) (any, error) {
		if err := strictjson.Decode(raw, &struct{}{}); err != nil {
			return nil, peer.NewError(peer.CodeInvalidParams, err.Error())
		}
		if from.Hello().Role != fakectl.RoleAdapter {
			return nil, peer.NewError(peer.CodeInvalidParams, "only adapters attach inputs")
		}
		ctx, cancel := context.WithCancel(ctx)
		finished := make(chan struct{})
		watching := make(chan struct{})
		go func() {
			defer close(watching)
			select {
			case <-from.Done():
				cancel()
			case <-finished:
			}
		}()
		detach, err := r.Attach(ctx, from.Hello().ID, RemoteKnobs{Conn: from})
		close(finished)
		<-watching
		cancel()
		if err != nil {
			return nil, err
		}
		go func() { <-from.Done(); detach() }()
		return struct{}{}, nil
	})
}

// ApplyInputs is the remote implementation of InputReceiver. The empty response
// is checked strictly; returning confirms installation in the adapter only.
func (r RemoteKnobs) ApplyInputs(ctx context.Context, in ExternalInputs) error {
	if err := in.validate(); err != nil {
		return err
	}
	var raw json.RawMessage
	// fakectl requires a nonempty args object. An explicit empty snapshot
	// encodes the complete zero value without changing that generic rule.
	wire := struct {
		PromptRefusal adapterproto.PromptRefusal  `json:"prompt_refusal,omitempty"`
		Availability  []adapterproto.Availability `json:"availability"`
	}{in.PromptRefusal, append([]adapterproto.Availability{}, in.Availability...)}
	if err := r.Do(ctx, KnobExternalInputs, wire, &raw); err != nil {
		return err
	}
	return strictjson.Decode(raw, &struct{}{})
}

var _ InputReceiver = (*Knobs)(nil)
var _ InputReceiver = RemoteKnobs{}

// decodeExternalInputs accepts an explicitly empty availability snapshot as well
// as absence: fakectl requires a nonempty argument object for the zero value.
func decodeExternalInputs(raw []byte) (any, error) {
	var wire struct {
		PromptRefusal adapterproto.PromptRefusal   `json:"prompt_refusal,omitempty"`
		Availability  *[]adapterproto.Availability `json:"availability,omitempty"`
	}
	if err := strictjson.Decode(raw, &wire); err != nil {
		return nil, err
	}
	in := ExternalInputs{PromptRefusal: wire.PromptRefusal}
	if wire.Availability != nil && len(*wire.Availability) != 0 {
		in.Availability = *wire.Availability
	}
	return in, in.validate()
}
