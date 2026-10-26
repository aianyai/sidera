package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/reply"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The delivery of the initial message of a session creation.
//
// The message is the body of the signal that asks for the session, and it is for the
// session, an endpoint that does not exist when the signal is judged. So the target
// makes a delivery of it for the session endpoint it allocates, as a message to that
// endpoint, and writes it with the control record in the transaction that takes the
// signal in (control-intake). From then on it is a message like any other: it waits in
// the queue of the session, is handed out by recv under a lease and is settled by an
// acknowledgement, and it is rejected, with its sender told, when the session is not
// made or is closed first.
//
// It is judged as the signal was, not as a message: it is let in because the creation
// was allowed, and when it is handed out the creation is judged again from what the
// delivery remembers of it (derivedAuth), by the runtime endpoint it was for.

// derivedAuth is the authorization request that let the delivery of an initial message
// in, as the delivery keeps it: the signal that asked and the preset it named. The
// runtime endpoint it was addressed to is the delivery's own record of where it comes
// from.
type derivedAuth struct {
	Signal adapterproto.ControlSignal `json:"signal"`
	Preset string                     `json:"preset"`
}

func encodeDerivedAuth(preset string) ([]byte, error) {
	return json.Marshal(derivedAuth{Signal: adapterproto.SignalSessionCreate, Preset: preset})
}

func decodeDerivedAuth(b []byte) (derivedAuth, error) {
	var a derivedAuth
	if err := json.Unmarshal(b, &a); err != nil {
		return a, fmt.Errorf("the authorization request of an initial message: %w", err)
	}
	if a.Signal != adapterproto.SignalSessionCreate || a.Preset == "" {
		return a, fmt.Errorf("the authorization request of an initial message is %+v", a)
	}
	return a, nil
}

// derivedDelivery is the delivery of the initial message a control record remembers,
// made from the record alone: the intake writes it, and the recovery rule that writes
// it again after a crash made the same one, because it is made here and nowhere else.
func derivedDelivery(c store.CtlRecord) (store.InRecord, error) {
	rec, err := delivery.NewTargetRecord(c.AcceptedAt, c.Record.AcceptBefore, c.Record.ReportBy, c.EndpointMaxQueue)
	if err != nil {
		return store.InRecord{}, fmt.Errorf("engine: the delivery of the initial message of %s: %w", c.Key.Message, err)
	}
	auth, err := encodeDerivedAuth(c.Preset)
	if err != nil {
		return store.InRecord{}, err
	}
	key := store.DerivedKey(c)
	body := c.Initial.Body
	return store.InRecord{
		Key: key, Record: rec,
		Dedup:         delivery.NewDedupRecord(delivery.DedupKey{SenderNode: key.SenderNode, Message: key.Message, Recipient: key.Recipient}, c.Dedup.Digest, c.Record.ReportBy),
		Auth:          delivery.Authorization{Kind: delivery.AuthorizationAllow},
		ReplyDecision: reply.Decision{Reason: reply.ReasonNoOutbound},
		Handout:       store.HandoutFields{ContentType: c.Initial.ContentType, SentAt: c.SentAt},
		AcceptedAt:    c.AcceptedAt,
		Body:          &body,
		Derived:       &store.DerivedInfo{From: c.Key.Recipient, AuthRequest: auth},
	}, nil
}

// derivedHandoutAuthorization is the second check of a delivery of an initial message,
// made when its body would be handed out: the creation it came with, judged again as
// a request to the runtime endpoint it was for, by that endpoint's declarations as
// they are now and, for a signal from another node, the rules of the node. A runtime
// endpoint the node no longer holds is a creation that cannot be judged, which is a
// refusal.
func (e *Engine) derivedHandoutAuthorization(now time.Time, in store.InRecord) (delivery.AuthorizationKind, error) {
	req, err := decodeDerivedAuth(in.Derived.AuthRequest)
	if err != nil {
		return 0, err
	}
	runtime := e.view.endpoints[in.Derived.From]
	if runtime == nil {
		return delivery.AuthorizationDeny, nil
	}
	action, err := adapterreg.ActionOf(req.Signal)
	if err != nil {
		return 0, err
	}
	var size int64
	if in.Body != nil {
		size = int64(in.Body.Len)
	}
	v := e.evaluate(now, authCheck{
		stage: authz.StageRecv, local: in.Key.SenderNode == e.d.Node,
		sender: authz.Sender{Node: in.Key.SenderNode, Endpoint: in.Key.Sender}, action: action,
		target: authz.Target{Kind: authz.TargetRuntime, Endpoint: in.Derived.From}, preset: req.Preset, bodySize: size,
		reply: in.ReplyDecision.Granted(), decl: runtime.decl,
	})
	auth := delivery.IntakeAuthorization(v).Kind
	if auth == 0 {
		return 0, fmt.Errorf("the check of the initial message %s gave no verdict", in.Key.Message)
	}
	return auth, nil
}

// failCreationWithoutMessage fails the creation of the control record of the key if it
// is not settled and the initial message it carried is gone: the delivery that held it
// was settled for damage, or is not there. The session would be made for a message that
// cannot be delivered, with nothing said, and the sender, who was told the signal was
// accepted, would wait for a message that is lost; so the reply says `failed` with
// control_unknown (no session will be attached, but a call already out may still make one
// that nobody controls, which is why the owner gets that alert when it is settled), and
// the endpoint being made is closed.
func (e *Engine) failCreationWithoutMessage(now time.Time, key store.TargetKey) error {
	c, ok := e.creatingIntent(key)
	if !ok || c.Initial == nil {
		return nil
	}
	if _, held := e.view.in[store.DerivedKey(c)]; held {
		return nil
	}
	failed := adapterproto.ControlCreateReply{Outcome: adapterproto.CreateReplyFailed, Reason: adapterproto.CreateReasonControlUnknown}
	return e.settleCreate(now, c, failed, nil)
}

// reconcileInitialMessage is what the start asks of a creation that is not settled and
// carries an initial message: the delivery that holds it is there and its body reads,
// or the creation fails. A body that does not read settles the delivery, which fails
// the creation by the same rule.
func (e *Engine) reconcileInitialMessage(now time.Time, c store.CtlRecord) error {
	if c.Initial == nil {
		return nil
	}
	if d, ok := e.view.in[store.DerivedKey(c)]; ok && d.Body != nil {
		if _, err := e.readBody(now, d, *d.Body); err != nil && !errors.Is(err, store.ErrBodyMismatch) && !errors.Is(err, store.ErrBodyMissing) {
			return err
		}
	}
	return e.failCreationWithoutMessage(now, c.Key)
}
