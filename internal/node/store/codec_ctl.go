package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var intentNames = names[IntentState]{
	{IntentCreating, "creating"}, {IntentCreated, "created"}, {IntentUnconfirmed, "unconfirmed"}, {IntentFailed, "failed"},
}

type intentJSON struct {
	Attempt   string                        `json:"attempt"`
	Endpoint  string                        `json:"endpoint"`
	State     string                        `json:"state"`
	NativeRef string                        `json:"native_ref,omitempty"`
	Process   *adapterproto.ProcessIdentity `json:"process,omitempty"`
}

type ctlJSON struct {
	Lost               bool            `json:"lost,omitempty"`
	DiscoveredAt       *wtime          `json:"discovered_at,omitempty"`
	Record             recordJSON      `json:"record"`
	Dedup              dedupJSON       `json:"dedup"`
	Kind               string          `json:"kind,omitempty"`
	Preset             string          `json:"preset,omitempty"`
	SessionEndpoint    string          `json:"session_endpoint,omitempty"`
	AcceptedAt         *wtime          `json:"accepted_at,omitempty"`
	EndpointMaxQueueNS int64           `json:"endpoint_max_queue_ns,omitempty"`
	SentAt             *wtime          `json:"sent_at,omitempty"`
	Body               *bodyRefJSON    `json:"body,omitempty"`
	Intent             *intentJSON     `json:"intent,omitempty"`
	ClosedEndpoint     string          `json:"closed_endpoint,omitempty"`
	Result             json.RawMessage `json:"result,omitempty"`
}

func checkIntent(in *CreateIntent) error {
	if err := in.Attempt.Validate(); err != nil {
		return fmt.Errorf("intent attempt: %w", err)
	}
	if in.Endpoint.IsZero() {
		return errors.New("an intent without the endpoint being created")
	}
	if _, err := intentNames.name(in.State, "intent state"); err != nil {
		return err
	}
	if (in.State == IntentCreated) != (in.NativeRef != "") {
		return errors.New("a native reference belongs to a created intent and to no other")
	}
	if in.Process != nil {
		if in.State != IntentCreated {
			return errors.New("a process identity belongs to a created intent")
		}
		if err := in.Process.Validate(); err != nil {
			return fmt.Errorf("intent process: %w", err)
		}
	}
	return nil
}

func encodeCtl(r CtlRecord) ([]byte, error) {
	if _, err := r.Key.Bytes(); err != nil {
		return nil, err
	}
	var j ctlJSON
	rj, err := toRecord(r.Record)
	if err != nil {
		return nil, err
	}
	if r.Record.Side != delivery.SideTarget || !r.Record.Control {
		return nil, errors.New("a control record holds a delivery of the target side that is a control signal")
	}
	j.Record = rj
	if j.Dedup, err = toDedup(r.Dedup, r.Key); err != nil {
		return nil, err
	}
	if r.Lost {
		if r.Kind != 0 || r.Preset != "" || !r.SessionEndpoint.IsZero() || !r.AcceptedAt.IsZero() || r.EndpointMaxQueue != 0 || !r.SentAt.IsZero() ||
			r.Body != nil || r.Intent != nil || !r.ClosedEndpoint.IsZero() || len(r.Result) != 0 {
			return nil, errors.New("a lost control record holds the parts of a regular one")
		}
		if j.DiscoveredAt = tptr(r.DiscoveredAt); j.DiscoveredAt == nil {
			return nil, errors.New("the discovery time is required")
		}
		j.Lost = true
		return marshal(j)
	}
	if !r.DiscoveredAt.IsZero() {
		return nil, errors.New("a regular control record has no discovery time")
	}
	if !r.Kind.IsControl() {
		return nil, fmt.Errorf("%s is not a control signal", r.Kind)
	}
	if j.Kind, err = enumName(r.Kind, envKindNames, "envelope kind"); err != nil {
		return nil, err
	}
	if (r.Kind == envelope.KindSessionCreate) != (r.Preset != "") {
		return nil, errors.New("a preset belongs to a session creation and to no other signal")
	}
	j.Preset = r.Preset
	if !r.SessionEndpoint.IsZero() {
		if r.Kind != envelope.KindSessionCreate {
			return nil, errors.New("a session endpoint belongs to a session creation")
		}
		j.SessionEndpoint = r.SessionEndpoint.String()
	}
	if j.AcceptedAt = tptr(r.AcceptedAt); j.AcceptedAt == nil {
		return nil, errors.New("the acceptance time is required")
	}
	if j.SentAt = tptr(r.SentAt); j.SentAt == nil {
		return nil, errors.New("the send time is required")
	}
	if r.EndpointMaxQueue < 0 {
		return nil, errors.New("a negative queue time")
	}
	j.EndpointMaxQueueNS = int64(r.EndpointMaxQueue)
	if j.Body, err = toBodyRef(r.Body); err != nil {
		return nil, err
	}
	if r.Intent != nil {
		if r.Kind != envelope.KindSessionCreate {
			return nil, errors.New("a creation intent belongs to a session creation")
		}
		if err := checkIntent(r.Intent); err != nil {
			return nil, err
		}
		sn, _ := intentNames.name(r.Intent.State, "intent state")
		j.Intent = &intentJSON{Attempt: string(r.Intent.Attempt), Endpoint: r.Intent.Endpoint.String(), State: sn, NativeRef: r.Intent.NativeRef, Process: r.Intent.Process}
	}
	if !r.ClosedEndpoint.IsZero() {
		if r.Kind != envelope.KindSessionClose {
			return nil, errors.New("a closed endpoint belongs to a session close")
		}
		j.ClosedEndpoint = r.ClosedEndpoint.String()
	}
	if len(r.Result) != 0 {
		if err := compactObject(r.Result, "the execution result", 0); err != nil {
			return nil, err
		}
		j.Result = json.RawMessage(r.Result)
	}
	return marshal(j)
}

func decodeCtl(key TargetKey, body []byte) (CtlRecord, error) {
	var j ctlJSON
	if err := unmarshal(body, &j); err != nil {
		return CtlRecord{}, err
	}
	r := CtlRecord{Key: key}
	var err error
	if r.Record, err = fromRecord(j.Record); err != nil {
		return r, err
	}
	if r.Record.Side != delivery.SideTarget || !r.Record.Control {
		return r, errors.New("a control record holds a delivery of the target side that is a control signal")
	}
	if r.Dedup, err = fromDedup(j.Dedup, key); err != nil {
		return r, err
	}
	if j.Lost {
		if j.Kind != "" || j.Preset != "" || j.SessionEndpoint != "" || j.AcceptedAt != nil || j.EndpointMaxQueueNS != 0 || j.SentAt != nil ||
			j.Body != nil || j.Intent != nil || j.ClosedEndpoint != "" || len(j.Result) != 0 {
			return r, errors.New("a lost control record holds the parts of a regular one")
		}
		r.Lost = true
		r.DiscoveredAt, err = tmust(j.DiscoveredAt, "discovered_at")
		return r, err
	}
	if j.DiscoveredAt != nil {
		return r, errors.New("a regular control record has no discovery time")
	}
	if r.Kind, err = enumValue(j.Kind, envKindNames, "envelope kind"); err != nil {
		return r, err
	}
	if !r.Kind.IsControl() {
		return r, fmt.Errorf("%s is not a control signal", r.Kind)
	}
	if (r.Kind == envelope.KindSessionCreate) != (j.Preset != "") {
		return r, errors.New("a preset belongs to a session creation and to no other signal")
	}
	r.Preset = j.Preset
	if j.SessionEndpoint != "" {
		if r.Kind != envelope.KindSessionCreate {
			return r, errors.New("a session endpoint belongs to a session creation")
		}
		if r.SessionEndpoint, err = ident.ParseEndpointID(j.SessionEndpoint); err != nil {
			return r, err
		}
	}
	if r.AcceptedAt, err = tmust(j.AcceptedAt, "accepted_at"); err != nil {
		return r, err
	}
	if r.SentAt, err = tmust(j.SentAt, "sent_at"); err != nil {
		return r, err
	}
	if j.EndpointMaxQueueNS < 0 {
		return r, errors.New("a negative queue time")
	}
	r.EndpointMaxQueue = time.Duration(j.EndpointMaxQueueNS)
	if r.Body, err = fromBodyRef(j.Body); err != nil {
		return r, err
	}
	if j.Intent != nil {
		if r.Kind != envelope.KindSessionCreate {
			return r, errors.New("a creation intent belongs to a session creation")
		}
		in := &CreateIntent{Attempt: adapterproto.AttemptID(j.Intent.Attempt), NativeRef: j.Intent.NativeRef, Process: j.Intent.Process}
		if in.Endpoint, err = ident.ParseEndpointID(j.Intent.Endpoint); err != nil {
			return r, err
		}
		if in.State, err = intentNames.value(j.Intent.State, "intent state"); err != nil {
			return r, err
		}
		if err := checkIntent(in); err != nil {
			return r, err
		}
		r.Intent = in
	}
	if j.ClosedEndpoint != "" {
		if r.Kind != envelope.KindSessionClose {
			return r, errors.New("a closed endpoint belongs to a session close")
		}
		if r.ClosedEndpoint, err = ident.ParseEndpointID(j.ClosedEndpoint); err != nil {
			return r, err
		}
	}
	if len(j.Result) != 0 {
		if err := compactObject(j.Result, "the execution result", 0); err != nil {
			return r, err
		}
		r.Result = []byte(j.Result)
	}
	return r, nil
}
