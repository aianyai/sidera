package protocol_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// schemaVerdict says whether the schemas accept a document of the given kind:
// "request", "notification", "result", "error", "frame" (message schema only) or
// "manifest". For a result the method names the definition of the result.
func schemaVerdict(tb testing.TB, kind, method string, raw []byte) error {
	tb.Helper()
	s := schemas(tb)
	doc, err := parse(raw)
	if err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	switch kind {
	case "manifest":
		return s.accepts(tb, "manifest.schema.json", doc)
	case "frame", "error":
		return s.accepts(tb, "message.schema.json", doc)
	}
	if err := s.accepts(tb, "message.schema.json", doc); err != nil {
		return err
	}
	switch kind {
	case "request":
		return s.accepts(tb, "methods.schema.json#/$defs/requests", doc)
	case "notification":
		return s.accepts(tb, "methods.schema.json#/$defs/notifications", doc)
	case "result":
		sm, ok := methodByName(tb)[method]
		if !ok || sm.ResultDef == "" {
			return fmt.Errorf("%q has no result definition", method)
		}
		obj, _ := doc.(map[string]any)
		return s.accepts(tb, "methods.schema.json#/$defs/"+sm.ResultDef, obj["result"])
	}
	tb.Fatalf("unknown kind %q", kind)
	return nil
}

// goVerdict says whether the Go types accept a document of the given kind, and
// returns the decoded value of the part that was checked.
func goVerdict(kind, method string, raw []byte) (adapterproto.Message, error) {
	switch kind {
	case "manifest":
		_, err := manifest.Parse(raw)
		return nil, err
	case "frame":
		return nil, errors.New("frames are the peer's business")
	}
	var f frame
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if kind == "error" {
		e, err := decodeError(f.Error)
		return e, err
	}
	if method == "" {
		method = f.Method
	}
	info, ok := adapterproto.Lookup(method)
	if !ok {
		return nil, fmt.Errorf("unknown method %q", method)
	}
	switch kind {
	case "request", "notification":
		if info.Notification != (kind == "notification") {
			return nil, fmt.Errorf("%s is not a %s", method, kind)
		}
		return info.DecodeParams(f.Params)
	case "result":
		return info.DecodeResult(f.Result)
	}
	return nil, fmt.Errorf("unknown kind %q", kind)
}

// partVerdict is goVerdict for a bare params or result document, as the
// differential tests use it.
type target struct {
	Name      string
	SchemaRef string
	Go        func(raw []byte) (any, error)
}

func buildTargets(tb testing.TB) []target {
	tb.Helper()
	var out []target
	for _, sm := range schemaMethods(tb) {
		info, ok := adapterproto.Lookup(sm.Name)
		if !ok {
			tb.Fatalf("method %s is in the schema but not in Go", sm.Name)
		}
		out = append(out, target{
			Name:      sm.Name + " params",
			SchemaRef: "methods.schema.json#/$defs/" + sm.ParamsDef,
			Go:        func(raw []byte) (any, error) { return info.DecodeParams(raw) },
		})
		if sm.ResultDef != "" {
			out = append(out, target{
				Name:      sm.Name + " result",
				SchemaRef: "methods.schema.json#/$defs/" + sm.ResultDef,
				Go:        func(raw []byte) (any, error) { return info.DecodeResult(raw) },
			})
		}
	}
	add := func(name, ref string, fn func(raw []byte) (any, error)) {
		out = append(out, target{Name: name, SchemaRef: ref, Go: fn})
	}
	decode := func(v adapterproto.Message) func([]byte) (any, error) {
		return func(raw []byte) (any, error) {
			err := adapterproto.Decode(raw, v)
			return v, err
		}
	}
	add("ProcessIdentity", "types.schema.json#/$defs/ProcessIdentity", func(raw []byte) (any, error) {
		return decode(new(adapterproto.ProcessIdentity))(raw)
	})
	add("Capabilities", "types.schema.json#/$defs/Capabilities", func(raw []byte) (any, error) {
		return decode(new(adapterproto.Capabilities))(raw)
	})
	add("RecvPage", "types.schema.json#/$defs/RecvPage", func(raw []byte) (any, error) {
		return decode(new(adapterproto.RecvPage))(raw)
	})
	add("ControlReply", "types.schema.json#/$defs/ControlReply", func(raw []byte) (any, error) {
		return decode(new(adapterproto.ControlReply))(raw)
	})
	add("ErrorObject", "message.schema.json#/$defs/ErrorObject", func(raw []byte) (any, error) {
		return decode(new(adapterproto.ErrorObject))(raw)
	})
	add("manifest", "manifest.schema.json", func(raw []byte) (any, error) {
		return manifest.Parse(raw)
	})
	return out
}
