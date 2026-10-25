package fakeadapter_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The schema files of protocol/adapter/v1 are the oracle of the consistency
// tests: they are written by hand, they are what an adapter in any language
// implements, and nothing in this package produced them.

const (
	schemaDir = "../../../../protocol/adapter/v1"
	idBase    = "https://sidera.invalid/adapter/v1/"
)

type schemaSet struct {
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	cache    map[string]*jsonschema.Schema
}

var loadedSchemas = sync.OnceValues(func() (*schemaSet, error) {
	c := jsonschema.NewCompiler()
	for _, name := range []string{"message", "types", "methods", "manifest"} {
		file := filepath.Join(schemaDir, name+".schema.json")
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if err := c.AddResource(idBase+name+".schema.json", doc); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	}
	return &schemaSet{compiler: c, cache: map[string]*jsonschema.Schema{}}, nil
})

func schemas(t testing.TB) *schemaSet {
	t.Helper()
	s, err := loadedSchemas()
	if err != nil {
		t.Fatalf("load schemas: %v", err)
	}
	return s
}

// accepts reports why the schema at ref rejects the document, or nil.
func (s *schemaSet) accepts(t testing.TB, ref string, doc any) error {
	t.Helper()
	s.mu.Lock()
	sch, ok := s.cache[ref]
	if !ok {
		var err error
		sch, err = s.compiler.Compile(idBase + ref)
		if err != nil {
			s.mu.Unlock()
			t.Fatalf("compile %s: %v", ref, err)
		}
		s.cache[ref] = sch
	}
	s.mu.Unlock()
	return sch.Validate(doc)
}

func parseJSON(raw []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// wireFrame is one line of the protocol with its members raw.
type wireFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func (f wireFrame) isRequest() bool      { return f.Method != "" && len(f.ID) > 0 }
func (f wireFrame) isNotification() bool { return f.Method != "" && len(f.ID) == 0 }

// resultDefinition is the name of the definition in methods.schema.json that
// describes the result of a request method.
func resultDefinition(t testing.TB, method string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir, "methods.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs struct {
			Requests struct {
				OneOf []struct {
					Properties struct {
						Method struct {
							Const string `json:"const"`
						} `json:"method"`
					} `json:"properties"`
					Result string `json:"x-result"`
				} `json:"oneOf"`
			} `json:"requests"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, e := range doc.Defs.Requests.OneOf {
		if e.Properties.Method.Const == method {
			return e.Result
		}
	}
	t.Fatalf("methods.schema.json has no request %q", method)
	return ""
}

// checkFrame validates one line against the schemas and against the Go types of
// the protocol. requestMethod names the method of the request this line answers
// (empty for a request or a notification).
func checkFrame(t testing.TB, line []byte, requestMethod string) error {
	t.Helper()
	s := schemas(t)
	doc, err := parseJSON(line)
	if err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	if err := s.accepts(t, "message.schema.json", doc); err != nil {
		return fmt.Errorf("message schema: %w", err)
	}
	var f wireFrame
	if err := json.Unmarshal(line, &f); err != nil {
		return err
	}
	switch {
	case f.isRequest() || f.isNotification():
		if err := s.accepts(t, "methods.schema.json", doc); err != nil {
			return fmt.Errorf("methods schema: %w", err)
		}
		info, ok := adapterproto.Lookup(f.Method)
		if !ok {
			return fmt.Errorf("method %q is not in the protocol", f.Method)
		}
		if _, err := info.DecodeParams(f.Params); err != nil {
			return fmt.Errorf("params of %s do not decode: %w", f.Method, err)
		}
	case len(f.Error) > 0:
		var e adapterproto.ErrorObject
		if err := adapterproto.Decode(f.Error, &e); err != nil {
			return fmt.Errorf("error object does not decode: %w", err)
		}
	case len(f.Result) > 0:
		if requestMethod == "" {
			return fmt.Errorf("a result for a request that was never seen")
		}
		def := resultDefinition(t, requestMethod)
		obj, _ := doc.(map[string]any)
		if err := s.accepts(t, "methods.schema.json#/$defs/"+def, obj["result"]); err != nil {
			return fmt.Errorf("result of %s: %w", requestMethod, err)
		}
		info, _ := adapterproto.Lookup(requestMethod)
		if _, err := info.DecodeResult(f.Result); err != nil {
			return fmt.Errorf("result of %s does not decode: %w", requestMethod, err)
		}
	default:
		return fmt.Errorf("a line that is neither a message nor a response")
	}
	return nil
}
