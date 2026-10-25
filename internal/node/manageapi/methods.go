package manageapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/apiwire"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/sdk/go/strictjson"
)

// A method is a row of the table of the interface: its name on the wire, the
// catalogue entry it is authorized as, and how its params are read. The handling
// of a request is the same for every row; a row has no rule of who may do it.
type method struct {
	name string
	// op is the catalogue entry of the method. A method whose operation does not
	// change state is a read; one that does comes through the engine's door.
	op admin.Operation
	// plan reads the params, a JSON object, and says what the request asks for.
	plan func(s *Server, params json.RawMessage) (*plan, error)
}

// plan is a request's params read. canonical is their encoding in the one form
// that equal requests share, which the fingerprint is made of.
type plan struct {
	canonical []byte
	// operation is what the engine is asked to do, for a method that changes state.
	operation engine.Operation
	// read answers a read from the engine.
	read func(ctx context.Context, svc engine.ManageService) (any, error)
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
// the place where the owner of another operation adds a row.
func defaultMethods() map[string]*method {
	ms := map[string]*method{}
	for _, m := range []*method{endpointsList, endpointsClose, daemonStop} {
		ms[m.name] = m
	}
	return ms
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
		return &plan{canonical: canonical(p), read: func(ctx context.Context, svc engine.ManageService) (any, error) {
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
				out.Endpoints = append(out.Endpoints, endpointJSON{
					Address: v.Address.String(), Kind: v.Kind.String(), Instance: v.Instance,
					State: stateName(v.Status), Queued: v.Queued, Draining: v.Draining,
				})
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
			return nil, &engine.Refusal{Reason: engine.ReasonEndpointUnknown}
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
