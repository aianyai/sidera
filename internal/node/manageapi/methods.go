package manageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// A method is a row of the table of the interface: its name on the wire, the
// catalogue entry it is authorized as, and how its params are read. The handling
// of a request is the same for every row; a row has no rule of who may do it.
type method struct {
	name string
	// op is the catalogue entry of the method. A method whose operation is
	// replay protected comes through the engine's door; any other is answered by
	// a direct call of the engine, with the envelope of a read.
	op admin.Operation
	// plan reads the params, a JSON object, and says what the request asks for.
	plan func(s *Server, params json.RawMessage) (*plan, error)

	// test, when set, makes the row a test row instead (plan and op are then
	// unset): the door of the test harness to the node it runs in, answered as a
	// read whose result is what test returns. A test row is outside the catalogue
	// of administrative operations and is not judged by admin.Authorize, because
	// it is no operation of the owner's: it has no catalogue entry (the shipped
	// catalogue must have none), carries no operation identifier and stores no
	// result. It is safe to leave out of Authorize because the rows exist only in a
	// binary built with a test tag (testMethods), which is never shipped, and the
	// socket it is on is the user's own, as for every method. Its name begins with
	// "test.".
	test func(s *Server, ctx context.Context, params json.RawMessage) (any, error)
}

// testPrefix begins the name of every test row, so that a test row is never
// mistaken for a method of the owner's and a method of the owner's never takes a
// name of the harness.
const testPrefix = "test."

// plan is a request's params read. canonical is their encoding in the one form
// that equal requests share, which the fingerprint is made of.
type plan struct {
	canonical []byte
	// operation is what the engine is asked to do, for a method that changes state.
	operation engine.Operation
	// direct answers a request that is not replay protected from the engine at
	// once, as a read is answered: a read, or a method that changes state and
	// keeps its own idempotency. Its result is worded as a read's, and nothing
	// of it is stored.
	direct func(ctx context.Context, svc engine.ManageService) (any, error)
}

// decodeParams reads the params of a method into p with the strict rules. The
// params are decoded in the place they have in the request, so that a fault is
// named by its path from the top ("params.endpoint").
func decodeParams[P any](raw json.RawMessage, p *P) error {
	var wrapped struct {
		Params P `json:"params"`
	}
	doc := append(append([]byte(`{"params":`), raw...), '}')
	if err := strictjson.Decode(doc, &wrapped); err != nil {
		return err
	}
	*p = wrapped.Params
	return nil
}

// checkPlan refuses a plan that is not what the row's operation authorized. The
// request is authorized by the row's op before its params are read, and what runs
// is the plan's operation, which the engine's door judges by its own Admin(): were
// the two different, a request would be authorized as one catalogue entry and
// carried out as another. A row that is replay protected must plan an operation whose
// Admin() is its op and no direct call; a row that is not must plan a direct call
// and no operation. A row that breaks this is a defect of the program, not of the request.
func (m *method) checkPlan(p *plan) error {
	if m.op.ReplayProtected() {
		switch {
		case p.operation == nil || p.direct != nil:
			return fmt.Errorf("manageapi: %q is authorized as a replay-protected operation and plans none", m.name)
		case p.operation.Admin() != m.op:
			return fmt.Errorf("manageapi: %q is authorized as %+v and would run as %+v", m.name, m.op, p.operation.Admin())
		}
		return nil
	}
	if p.operation != nil || p.direct == nil {
		return fmt.Errorf("manageapi: %q is authorized as a request that is not replay protected and does not plan a direct call", m.name)
	}
	return nil
}

// canonical is the encoding of read params that equal requests share: the fields
// of the type in the order it declares them.
func canonical(p any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		panic(fmt.Sprintf("manageapi: params that cannot be encoded: %v", err))
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// defaultMethods is the table: the methods of the operations the daemon has, and
// the place where the owner of another operation adds a row. The test rows are
// added from testMethods, which has none in a binary built without a test tag.
func defaultMethods() (map[string]*method, error) {
	ms := map[string]*method{}
	rows := []*method{
		status, alertsList, alertsAck,
		identityExportRevocation, identityConfirmBackup, identityRetire, clockClearRollback,
		nodeShow, nodeSet,
		networkShow, networkAddressSet, networkAddressRemove, networkListenSet,
		adaptersInstall, adaptersList, adaptersEnable, adaptersConfig, adaptersDisable, adaptersRemove,
		presetsList, presetsAdd, presetsRemove,
		trustExpect, trustForget, trustReject, trustRequest, trustList, trustRemove, trustClear,
		endpointsList, endpointsClose, endpointsSet, sessionsAttach, daemonStop, operationResult,
	}
	for _, m := range append(rows, testMethods()...) {
		if err := checkRow(m); err != nil {
			return nil, err
		}
		if ms[m.name] != nil {
			return nil, fmt.Errorf("manageapi: two methods named %q", m.name)
		}
		ms[m.name] = m
	}
	return ms, nil
}

// checkRow refuses a row that is neither an operation of the owner's nor a test
// row, or is both, or whose name does not say which it is.
func checkRow(m *method) error {
	isTest := m.test != nil
	switch {
	case m.name == "":
		return errors.New("manageapi: a method without a name")
	case isTest && (m.plan != nil || m.op.Kind != admin.OpUnknown):
		return fmt.Errorf("manageapi: %q is a test row and an operation of the owner's", m.name)
	case !isTest && m.plan == nil:
		return fmt.Errorf("manageapi: %q does nothing", m.name)
	case isTest != strings.HasPrefix(m.name, testPrefix):
		return fmt.Errorf("manageapi: %q: only test rows are named %s...", m.name, testPrefix)
	}
	return nil
}

// endpoints.list: the endpoints of the node, closed ones included, in the order of
// their identifiers; an instance filters them. Never a token.
var endpointsList = &method{
	name: "endpoints.list", op: admin.Operation{Kind: admin.OpRead},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Instance string `json:"instance,omitempty"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		return &plan{canonical: canonical(p), direct: func(ctx context.Context, svc engine.ManageService) (any, error) {
			views, err := svc.Endpoints(ctx)
			if err != nil {
				return nil, err
			}
			out := struct {
				Endpoints []endpointJSON `json:"endpoints"`
			}{Endpoints: []endpointJSON{}}
			for _, v := range views {
				if p.Instance != "" && v.Instance != p.Instance {
					continue
				}
				j := endpointJSON{
					Address: v.Address.String(), Kind: v.Kind.String(), Instance: v.Instance,
					State: stateName(v.Status), Queued: v.Queued, Draining: v.Draining,
				}
				if v.Kind == adapterreg.KindSession {
					j.Origin = v.Origin.String()
				}
				out.Endpoints = append(out.Endpoints, j)
			}
			return out, nil
		}}, nil
	},
}

type endpointJSON struct {
	Address  string `json:"address"`
	Kind     string `json:"kind"`
	Instance string `json:"instance"`
	State    string `json:"state"`
	Queued   int    `json:"queued"`
	Draining bool   `json:"draining"`
	// Origin is "created" or "attached", for a session endpoint only.
	Origin string `json:"origin,omitempty"`
}

func stateName(s store.EndpointStatus) string {
	switch s {
	case store.EndpointCreating:
		return "creating"
	case store.EndpointOpen:
		return "open"
	case store.EndpointClosed:
		return "closed"
	}
	return fmt.Sprintf("invalid(%d)", uint8(s))
}

// endpoints.close: close one endpoint of this node. A session endpoint, a direct
// endpoint and a runtime endpoint each get the answer the engine gives; an address
// of another node is no endpoint of this one.
var endpointsClose = &method{
	name: "endpoints.close", op: admin.Operation{Kind: admin.OpManageEndpointSession},
	plan: func(s *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Endpoint string `json:"endpoint"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		addr, err := ident.ParseAddress(p.Endpoint)
		if err != nil {
			return nil, apiwire.Malformed("the value is not the address of an endpoint", "params.endpoint")
		}
		if addr.Node != s.cfg.Node {
			return nil, apiwire.EndpointUnknown()
		}
		p.Endpoint = addr.String()
		op := engine.CloseEndpointOp(addr.Endpoint, func(o engine.CloseOutcome) ([]byte, error) {
			return json.Marshal(struct {
				Endpoint string `json:"endpoint"`
				Outcome  string `json:"outcome"`
			}{p.Endpoint, o.String()})
		})
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// sessions.attach: attach a session a person opened to an enabled instance of its
// adapter, and answer with the endpoint, the instance, the token of the endpoint and
// whether the adapter can deliver prompts to the session (the wake state). It is the
// one method whose answer carries a token. The request is idempotent by the instance
// and the native session and is repeated by design, so it is no operation of the
// engine's door: no operation identifier, no expected version, nothing stored. The wake
// information is opaque, is never logged and is not part of any error.
var sessionsAttach = &method{
	name: "sessions.attach", op: admin.Operation{Kind: admin.OpAttachSession},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			Adapter     string                `json:"adapter"`
			Instance    string                `json:"instance,omitempty"`
			NativeRef   string                `json:"native_ref"`
			DisplayName string                `json:"display_name,omitempty"`
			Wake        adapterproto.WakeInfo `json:"wake,omitempty"`
			// Create is true when absent; false only finds the endpoint the session has.
			Create *bool `json:"create,omitempty"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		req := engine.AttachRequest{
			Adapter: p.Adapter, Instance: p.Instance, NativeRef: p.NativeRef, DisplayName: p.DisplayName, Wake: p.Wake,
			FindOnly: p.Create != nil && !*p.Create,
		}
		return &plan{canonical: canonical(p), direct: func(ctx context.Context, svc engine.ManageService) (any, error) {
			res, err := svc.AttachSession(ctx, req)
			if err != nil {
				return nil, err
			}
			return attachJSON{
				Endpoint: res.Endpoint.String(), Instance: res.Instance, Token: res.Token,
				Wake: wakeJSON{State: res.Wake.State, Reason: res.Wake.Reason, Detail: res.Wake.Detail},
			}, nil
		}}, nil
	},
}

type attachJSON struct {
	Endpoint string   `json:"endpoint"`
	Instance string   `json:"instance"`
	Token    string   `json:"token"`
	Wake     wakeJSON `json:"wake"`
}

type wakeJSON struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// daemon.stop: shut the daemon down normally, once the answer has been written.
var daemonStop = &method{
	name: "daemon.stop", op: admin.Operation{Kind: admin.OpStopDaemon},
	plan: func(_ *Server, raw json.RawMessage) (*plan, error) {
		var p struct{}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		op := engine.StopDaemonOp(func() ([]byte, error) { return []byte(`{"stopping":true}`), nil })
		return &plan{canonical: canonical(p), operation: op}, nil
	},
}

// operation.result: the result stored for an operation identifier of the requester,
// as the request that ran it was answered. It reads one place for every result,
// whether the operation kept it under state/admin/ or in a versioned file of
// config/, and only under the requester's own key: an identifier of another person
// is another key. An identifier with no result is operation_unknown; the same
// answer is given for one that never ran and for one whose request was refused,
// which stored nothing.
var operationResult = &method{
	name: "operation.result", op: admin.Operation{Kind: admin.OpQueryOperationResult},
	plan: func(s *Server, raw json.RawMessage) (*plan, error) {
		var p struct {
			OperationID string `json:"operation_id,omitempty"`
		}
		if err := decodeParams(raw, &p); err != nil {
			return nil, err
		}
		id, err := parseOperationID(p.OperationID)
		if err != nil {
			return nil, err
		}
		if id.IsZero() {
			return nil, apiwire.FromAdmin(admin.ReasonMissingOperationID)
		}
		key := admin.KeyFor(s.actor(), id)
		return &plan{canonical: canonical(p), direct: func(ctx context.Context, svc engine.ManageService) (any, error) {
			result, ok, err := svc.OperationResult(ctx, key)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, apiwire.OperationUnknown()
			}
			return json.RawMessage(result), nil
		}}, nil
	},
}
