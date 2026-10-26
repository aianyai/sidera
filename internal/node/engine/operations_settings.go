package engine

import (
	"encoding/json"
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
)

// configRequest is what a versioned change of the configuration takes from the
// command that carries it.
func (x *opRun) configRequest() config.Request {
	return config.Request{Key: x.key, Fingerprint: x.fingerprint, Expected: x.expected}
}

// SetNodeSettingsOp is the operation that changes the settings of the node: a
// patch, a JSON object of settings, merged into the current ones and judged whole
// by the packages that own each rule. The version the request names is checked
// inside the change, and the operation with its result is kept in node.json in
// the same replacement. The engine reads the settings when it uses them, so a
// change takes effect with the next command. encode makes the result from the
// state the change produces; it runs in the loop and must not wait.
func SetNodeSettingsOp(patch []byte, encode func(config.Node) ([]byte, error)) Operation {
	return &setNodeSettingsOp{patch: patch, encode: encode}
}

type setNodeSettingsOp struct {
	patch  []byte
	encode func(config.Node) ([]byte, error)
}

func (o *setNodeSettingsOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpChangeNodeSettings}
}
func (o *setNodeSettingsOp) stops() bool { return false }
func (o *setNodeSettingsOp) run(e *Engine, x *opRun) ([]byte, error) {
	n, err := e.d.Config.SetNodeSettings(x.configRequest(), o.patch, func(after config.Node) (json.RawMessage, error) {
		b, err := o.encode(after)
		if err != nil {
			return nil, fmt.Errorf("the result of %s: %w", x.name, err)
		}
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	e.promptsDirty = true // the cap on the turns that prompts start may have been raised
	return n.LastOperation.Result, nil
}

// EndpointChange is what the owner's change of the settings of an endpoint
// produced, for the result to say.
type EndpointChange struct {
	Endpoint ident.EndpointID
	// Settings are what the owner has given the endpoint now, those it left to the
	// node default not among them.
	Settings config.EndpointOverride
	// Version is the version of the instances after the change.
	Version admin.Version
}

// SetEndpointSettingsOp is the operation that changes the settings of one
// endpoint of the node: a patch of its queue time and backlog limit, each judged by
// the package that owns the rule. The endpoint is found in the view, which says
// which adapter instance it belongs to and whether it is open; a setting is kept
// for a stable endpoint or one that is open now. The version is that of the
// instances, and the operation with its result is kept in instances.json in the
// same replacement. encode makes the result; it runs in the loop and must not wait.
func SetEndpointSettingsOp(endpoint ident.EndpointID, patch []byte, encode func(EndpointChange) ([]byte, error)) Operation {
	return &setEndpointSettingsOp{endpoint: endpoint, patch: patch, encode: encode}
}

type setEndpointSettingsOp struct {
	endpoint ident.EndpointID
	patch    []byte
	encode   func(EndpointChange) ([]byte, error)
}

func (o *setEndpointSettingsOp) Admin() admin.Operation {
	return admin.Operation{Kind: admin.OpChangeEndpointSettings}
}
func (o *setEndpointSettingsOp) stops() bool { return false }
func (o *setEndpointSettingsOp) run(e *Engine, x *opRun) ([]byte, error) {
	st := e.view.endpoints[o.endpoint]
	if st == nil {
		return nil, refuse(ReasonEndpointUnknown)
	}
	instance := st.rec.Instance
	after, err := e.d.Config.SetEndpointSettings(x.configRequest(), instance, o.endpoint, o.patch, e.endpointOpen,
		func(in config.Instances) (json.RawMessage, error) {
			b, err := o.encode(EndpointChange{Endpoint: o.endpoint, Settings: in.Items[instance].Endpoints[o.endpoint], Version: in.Version})
			if err != nil {
				return nil, fmt.Errorf("the result of %s: %w", x.name, err)
			}
			return b, nil
		})
	if err != nil {
		return nil, err
	}
	return after.LastOperation.Result, nil
}
