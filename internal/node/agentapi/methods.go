package agentapi

import (
	"context"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// The HTTP request accepts waits longer than the runtime limit; only the engine
// clamps them. This boundary caps the input before converting seconds.
const maxRequestedWaitSeconds = 24 * 60 * 60

// Declaration is the endpoint's self-declared restrictions in the Agent wire.
type Declaration struct {
	Paused               bool     `json:"paused"`
	RefuseMultiRecipient bool     `json:"refuse_multi_recipient"`
	RestrictSenders      bool     `json:"restrict_senders"`
	AcceptFrom           []string `json:"accept_from"`
	HiddenFromDirectory  bool     `json:"hidden_from_directory"`
}

// DeclarationPatch changes only the fields provided. AcceptFrom replaces its list.
type DeclarationPatch struct {
	Paused               *bool     `json:"paused,omitempty"`
	RefuseMultiRecipient *bool     `json:"refuse_multi_recipient,omitempty"`
	RestrictSenders      *bool     `json:"restrict_senders,omitempty"`
	AcceptFrom           *[]string `json:"accept_from,omitempty"`
	HiddenFromDirectory  *bool     `json:"hidden_from_directory,omitempty"`
}

// DeclareResult is the declaration after the patch was applied.
type DeclareResult struct {
	Declaration Declaration `json:"declaration"`
}

// Budget is one front end's output limit, with the shared wire unit name.
type Budget struct {
	Unit  string `json:"unit"`
	Limit int    `json:"limit"`
}

// Frontend describes the receive limits declared by an endpoint's adapter.
type Frontend struct {
	RecvWaitMaxS int64  `json:"recv_wait_max_s"`
	Budget       Budget `json:"budget"`
}

// MeResult describes the authenticated endpoint and only its declared front ends.
type MeResult struct {
	Address     string                             `json:"address"`
	Node        string                             `json:"node"`
	Kind        string                             `json:"kind"`
	DisplayName string                             `json:"display_name"`
	Declaration Declaration                        `json:"declaration"`
	Frontends   map[adapterproto.Frontend]Frontend `json:"frontends"`
}

// SendRequest sends one message. ContentType and AcceptBeforeS take the engine's
// defaults when omitted. IdempotencyKey is chosen by the caller, never generated.
type SendRequest struct {
	To                 []string `json:"to"`
	Body               string   `json:"body"`
	ContentType        string   `json:"content_type,omitempty"`
	Thread             string   `json:"thread,omitempty"`
	DiscloseRecipients bool     `json:"disclose_recipients,omitempty"`
	AcceptBeforeS      int64    `json:"accept_before_s,omitempty"`
	IdempotencyKey     string   `json:"idempotency_key,omitempty"`
}

// ControlRequest sends one control signal. A creation can omit its initial
// message altogether; a present Body must be non-empty.
type ControlRequest struct {
	To             string                     `json:"to"`
	Signal         adapterproto.ControlSignal `json:"signal"`
	Preset         string                     `json:"preset,omitempty"`
	Body           string                     `json:"body,omitempty"`
	ContentType    string                     `json:"content_type,omitempty"`
	AcceptBeforeS  int64                      `json:"accept_before_s,omitempty"`
	IdempotencyKey string                     `json:"idempotency_key,omitempty"`
}

// ControlResult names the accepted signal. Its eventual control reply is read
// with Status, and is not part of this synchronous acceptance.
type ControlResult struct {
	MessageID adapterproto.MessageID `json:"message_id"`
	To        string                 `json:"to"`
	Replayed  bool                   `json:"replayed"`
}

// StatusRequest identifies one message sent by the authenticated endpoint.
type StatusRequest struct {
	MessageID adapterproto.MessageID `json:"message_id"`
}

// ReplyRequest answers a received message. The engine chooses its recipient.
type ReplyRequest struct {
	MessageID      adapterproto.MessageID `json:"message_id"`
	Body           string                 `json:"body"`
	ContentType    string                 `json:"content_type,omitempty"`
	Thread         string                 `json:"thread,omitempty"`
	IdempotencyKey string                 `json:"idempotency_key,omitempty"`
}

// SendResult identifies the durable message and its recipients, including a
// replay of an earlier request under the same explicit idempotency key.
type SendResult struct {
	MessageID adapterproto.MessageID `json:"message_id"`
	To        []string               `json:"to"`
	Replayed  bool                   `json:"replayed"`
}

// RecvRequest asks for a page or a particular part. A present WaitS, including
// zero, cannot be combined with Msg and Part. The engine clamps the wait.
type RecvRequest struct {
	Frontend adapterproto.Frontend  `json:"frontend"`
	WaitS    *int64                 `json:"wait_s,omitempty"`
	Msg      adapterproto.MessageID `json:"msg,omitempty"`
	Part     int                    `json:"part,omitempty"`
	AckLease adapterproto.LeaseRef  `json:"ack_lease,omitempty"`
}

// AckRequest acknowledges the named parts, or the entire page when Items is
// omitted. The engine alone decides which parts were handed to this endpoint.
type AckRequest struct {
	Lease adapterproto.LeaseRef  `json:"lease"`
	Items []adapterproto.PartRef `json:"items,omitempty"`
}

// AckResult lists exactly the parts acknowledged. Items is non-nil on success
// and may be empty when a forgotten page contains only chunked messages.
type AckResult struct {
	Items []adapterproto.PartAck `json:"items"`
}

// DirectoryRequest selects a node and a live continuation. Zero Limit takes the
// engine's default page size; the cursor is opaque and never grants permission.
type DirectoryRequest struct {
	Node   string `json:"node,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// DirectoryEntry describes a visible endpoint and the actions it accepts.
type DirectoryEntry struct {
	Address     string   `json:"address"`
	Kind        string   `json:"kind"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Accepts     []string `json:"accepts"`
}

// DirectoryResult is a live page. An empty NextCursor ends the listing.
type DirectoryResult struct {
	Endpoints  []DirectoryEntry `json:"endpoints"`
	NextCursor string           `json:"next_cursor"`
}

// Peer names a visible node. Self identifies the node serving this interface.
type Peer struct {
	Node string `json:"node"`
	Self bool   `json:"self"`
}

// PeersResult lists the nodes visible to the authenticated endpoint.
type PeersResult struct {
	Nodes []Peer `json:"nodes"`
}

func (r RecvRequest) engineRequest() (engine.RecvRequest, error) {
	if !r.Frontend.Valid() {
		return engine.RecvRequest{}, apiwire.Malformed("the front end is not cli or mcp", "frontend")
	}
	var wait time.Duration
	if r.WaitS != nil {
		if *r.WaitS < 0 || *r.WaitS > maxRequestedWaitSeconds {
			return engine.RecvRequest{}, apiwire.Malformed("the wait must be between zero and one day in seconds", "wait_s")
		}
		if r.Msg != "" || r.Part != 0 {
			return engine.RecvRequest{}, apiwire.Malformed("a part is fetched without a wait", "wait_s")
		}
		wait = time.Duration(*r.WaitS) * time.Second
	}
	if r.Msg != "" || r.Part != 0 {
		if err := (adapterproto.PageNext{Msg: r.Msg, Part: r.Part}).Validate(); err != nil {
			return engine.RecvRequest{}, apiwire.Malformed("a message and a valid part number are required together", "part")
		}
	}
	return engine.RecvRequest{Frontend: r.Frontend, Wait: wait, Msg: r.Msg, Part: r.Part, AckLease: r.AckLease}, nil
}

func sendResult(r engine.SendResult) SendResult {
	to := make([]string, len(r.To))
	for i, a := range r.To {
		to[i] = a.String()
	}
	return SendResult{MessageID: adapterproto.MessageID(r.Message), To: to, Replayed: r.Replayed}
}

func acceptBefore(seconds int64) (time.Duration, error) {
	// Bound before multiplying: an overflowing duration could become a valid
	// shorter deadline. The engine owns the protocol limit and its enforcement.
	if seconds < 0 || seconds > int64(delivery.MaxAcceptBefore/time.Second) {
		return 0, apiwire.Malformed("the delivery deadline is outside the protocol limit", "accept_before_s")
	}
	return time.Duration(seconds) * time.Second, nil
}

func declaration(d authz.Declaration) Declaration {
	out := Declaration{
		Paused: d.Paused, RefuseMultiRecipient: d.RefuseMultiRecipient,
		RestrictSenders: d.RestrictSenders, HiddenFromDirectory: d.HiddenFromDirectory,
		AcceptFrom: make([]string, len(d.AcceptFrom)),
	}
	for i, a := range d.AcceptFrom {
		out.AcceptFrom[i] = a.String()
	}
	return out
}

func addresses(list []string, path string) ([]ident.Address, error) {
	out := make([]ident.Address, len(list))
	for i, text := range list {
		a, err := address(text, fmt.Sprintf("%s[%d]", path, i))
		if err != nil {
			return nil, err
		}
		out[i] = a
	}
	return out, nil
}

func address(text, path string) (ident.Address, error) {
	a, err := ident.ParseAddress(text)
	if err != nil {
		return ident.Address{}, apiwire.Malformed("an address is not in its required form", path)
	}
	return a, nil
}

func (s *Server) method(ctx context.Context, caller engine.Caller, name string, body []byte) (any, error) {
	switch name {
	case "peers":
		var in struct{}
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		nodes, err := s.svc.Peers(ctx, caller)
		if err != nil {
			return nil, err
		}
		out := PeersResult{Nodes: make([]Peer, len(nodes))}
		for i, node := range nodes {
			out.Nodes[i] = Peer{Node: node.Node.String(), Self: node.Self}
		}
		return out, nil
	case "me":
		var in struct{}
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		v, err := s.svc.Me(ctx, caller)
		if err != nil {
			return nil, err
		}
		out := MeResult{
			Address: v.Address.String(), Node: v.Address.Node.String(), Kind: v.Kind.String(),
			DisplayName: v.DisplayName, Declaration: declaration(v.Declaration),
			Frontends: make(map[adapterproto.Frontend]Frontend, len(v.Frontends)),
		}
		for f, limits := range v.Frontends {
			unit, err := apiwire.BudgetUnitName(limits.Budget.Unit)
			if err != nil {
				return nil, err
			}
			out.Frontends[f] = Frontend{RecvWaitMaxS: int64(limits.RecvWaitMax / time.Second), Budget: Budget{Unit: unit, Limit: limits.Budget.Limit}}
		}
		return out, nil
	case "directory":
		var in DirectoryRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		q := engine.DirectoryQuery{Cursor: in.Cursor, Limit: in.Limit}
		if in.Node != "" {
			id, err := ident.ParseNodeID(in.Node)
			if err != nil {
				return nil, apiwire.Malformed("the node identifier is not in its required form", "node")
			}
			q.Node = id
		}
		page, err := s.svc.Directory(ctx, caller, q)
		if err != nil {
			return nil, err
		}
		out := DirectoryResult{Endpoints: make([]DirectoryEntry, len(page.Endpoints)), NextCursor: page.NextCursor}
		for i, ep := range page.Endpoints {
			actions := make([]string, len(ep.Accepts))
			for j, a := range ep.Accepts {
				actions[j] = a.String()
			}
			out.Endpoints[i] = DirectoryEntry{Address: ep.Address.String(), Kind: ep.Kind.String(), DisplayName: ep.DisplayName, Description: ep.Description, Accepts: actions}
		}
		return out, nil
	case "ack":
		var in AckRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		if len(in.Items) > adapterproto.MaxAckItems {
			return nil, apiwire.Malformed("the acknowledgement has too many items", "items")
		}
		if err := in.Lease.Validate(); err != nil {
			return nil, apiwire.Malformed("the lease is not in its required form", "lease")
		}
		for i, p := range in.Items {
			if err := p.Validate(); err != nil {
				return nil, apiwire.Malformed("the part reference is not in its required form", fmt.Sprintf("items[%d]", i))
			}
		}
		items, err := s.svc.Ack(ctx, caller, engine.AckRequest{Lease: in.Lease, Items: in.Items})
		if err != nil {
			return nil, err
		}
		out := AckResult{Items: make([]adapterproto.PartAck, len(items))}
		copy(out.Items, items)
		return out, nil
	case "reply":
		var in ReplyRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		if err := in.MessageID.Validate(); err != nil {
			return nil, apiwire.Malformed("the message identifier is not in its required form", "message_id")
		}
		if in.ContentType == "" {
			in.ContentType = engine.DefaultContentType
		}
		r, err := s.svc.Reply(ctx, caller, engine.ReplyRequest{
			Message: delivery.MessageID(in.MessageID), Body: []byte(in.Body),
			ContentType: in.ContentType, Thread: in.Thread, IdempotencyKey: in.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		return sendResult(r), nil
	case "status":
		var in StatusRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		if err := in.MessageID.Validate(); err != nil {
			return nil, apiwire.Malformed("the message identifier is not in its required form", "message_id")
		}
		return s.svc.MessageStatus(ctx, caller, in.MessageID)
	case "control":
		var in ControlRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		to, err := address(in.To, "to")
		if err != nil {
			return nil, err
		}
		accept, err := acceptBefore(in.AcceptBeforeS)
		if err != nil {
			return nil, err
		}
		r, err := s.svc.Control(ctx, caller, engine.ControlRequest{
			To: to, Signal: in.Signal, Preset: in.Preset,
			Body: []byte(in.Body), ContentType: in.ContentType,
			AcceptBefore: accept, IdempotencyKey: in.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		return ControlResult{MessageID: adapterproto.MessageID(r.Message), To: r.To[0].String(), Replayed: r.Replayed}, nil
	case "recv":
		var in RecvRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		r, err := in.engineRequest()
		if err != nil {
			return nil, err
		}
		return s.svc.Recv(ctx, caller, r)
	case "send":
		var in SendRequest
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		if len(in.To) != 1 {
			return nil, apiwire.New(apiwire.CategoryInvalidArgument, "single_recipient_only", "a message must have exactly one recipient")
		}
		to, err := addresses(in.To, "to")
		if err != nil {
			return nil, err
		}
		accept, err := acceptBefore(in.AcceptBeforeS)
		if err != nil {
			return nil, err
		}
		if in.ContentType == "" {
			in.ContentType = engine.DefaultContentType
		}
		r, err := s.svc.Send(ctx, caller, engine.SendRequest{
			To: to, Body: []byte(in.Body), ContentType: in.ContentType,
			Thread: in.Thread, DiscloseRecipients: in.DiscloseRecipients,
			AcceptBefore: accept, IdempotencyKey: in.IdempotencyKey,
		})
		if err != nil {
			return nil, err
		}
		return sendResult(r), nil
	case "declare":
		var in DeclarationPatch
		if err := strictjson.Decode(body, &in); err != nil {
			return nil, err
		}
		p := engine.DeclarationPatch{
			Paused: in.Paused, RefuseMultiRecipient: in.RefuseMultiRecipient,
			RestrictSenders: in.RestrictSenders, HiddenFromDirectory: in.HiddenFromDirectory,
		}
		if in.AcceptFrom != nil {
			list, err := addresses(*in.AcceptFrom, "accept_from")
			if err != nil {
				return nil, err
			}
			p.AcceptFrom = &list
		}
		d, err := s.svc.Declare(ctx, caller, p)
		if err != nil {
			return nil, err
		}
		return DeclareResult{Declaration: declaration(d)}, nil
	}
	return nil, apiwire.New(apiwire.CategoryNotFound, apiwire.CodeMethodUnknown, "there is no such method")
}
