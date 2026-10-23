package protocol_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const (
	schemaDir = "adapter/v1"
	idBase    = "https://sidera.invalid/adapter/v1/"
)

// schemaSet compiles the four schema files once and answers "does this schema
// accept this document".
type schemaSet struct {
	compiler *jsonschema.Compiler
	mu       sync.Mutex
	cache    map[string]*jsonschema.Schema
}

var shared = sync.OnceValues(func() (*schemaSet, error) {
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

func schemas(tb testing.TB) *schemaSet {
	tb.Helper()
	s, err := shared()
	if err != nil {
		tb.Fatalf("load schemas: %v", err)
	}
	return s
}

// compile returns the schema at ref, a file name with an optional fragment, for
// example "methods.schema.json#/$defs/initialize.params".
func (s *schemaSet) compile(tb testing.TB, ref string) *jsonschema.Schema {
	tb.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if sch, ok := s.cache[ref]; ok {
		return sch
	}
	sch, err := s.compiler.Compile(idBase + ref)
	if err != nil {
		tb.Fatalf("compile %s: %v", ref, err)
	}
	s.cache[ref] = sch
	return sch
}

// parse reads a JSON document the way the schema validator wants it, with
// numbers kept exact.
func parse(raw []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

// accepts reports why the schema at ref rejects the document, or nil.
func (s *schemaSet) accepts(tb testing.TB, ref string, doc any) error {
	tb.Helper()
	return s.compile(tb, ref).Validate(doc)
}

// rawSchema reads a schema file as plain JSON.
func rawSchema(tb testing.TB, name string) map[string]any {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(schemaDir, name+".schema.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		tb.Fatal(err)
	}
	return m
}

func sub(tb testing.TB, m map[string]any, path ...string) map[string]any {
	tb.Helper()
	cur := m
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			tb.Fatalf("schema has no object at %s", strings.Join(path, "."))
		}
		cur = next
	}
	return cur
}

// schemaMethod is one entry of the requests or notifications list of the methods
// schema.
type schemaMethod struct {
	Name         string
	Direction    string
	Notification bool
	ParamsDef    string // name of the definition of the params
	ResultDef    string // name of the definition of the result, empty for a notification
}

func schemaMethods(tb testing.TB) []schemaMethod {
	tb.Helper()
	defs := sub(tb, rawSchema(tb, "methods"), "$defs")
	var out []schemaMethod
	for _, list := range []string{"requests", "notifications"} {
		entries, _ := sub(tb, defs, list)["oneOf"].([]any)
		for _, e := range entries {
			entry := e.(map[string]any)
			props := entry["properties"].(map[string]any)
			sm := schemaMethod{
				Name:         props["method"].(map[string]any)["const"].(string),
				Direction:    entry["x-direction"].(string),
				Notification: list == "notifications",
				ParamsDef:    strings.TrimPrefix(props["params"].(map[string]any)["$ref"].(string), "#/$defs/"),
			}
			if r, ok := entry["x-result"].(string); ok {
				sm.ResultDef = r
			}
			out = append(out, sm)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func methodByName(tb testing.TB) map[string]schemaMethod {
	tb.Helper()
	m := map[string]schemaMethod{}
	for _, sm := range schemaMethods(tb) {
		m[sm.Name] = sm
	}
	return m
}

// dirOf is the examples directory of a method.
func dirOf(method string) string { return strings.TrimPrefix(method, "$/") }

// example is one file under examples/.
type example struct {
	Path string
	Dir  string // first path element below examples/
	Kind string // request, result, error, notification, manifest or invalid
	Name string
	Raw  []byte
}

func loadExamples(tb testing.TB) []example {
	tb.Helper()
	root := filepath.Join(schemaDir, "examples")
	var out []example
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 2 || !strings.HasSuffix(parts[1], ".json") {
			return fmt.Errorf("unexpected example path %s", rel)
		}
		ex := example{Path: rel, Dir: parts[0], Name: strings.TrimSuffix(parts[1], ".json"), Raw: raw}
		switch ex.Dir {
		case "manifest", "invalid":
			ex.Kind = ex.Dir
		default:
			kind, _, ok := strings.Cut(ex.Name, "-")
			if !ok {
				return fmt.Errorf("example %s: name must start with request-, result-, error- or notification-", rel)
			}
			ex.Kind = kind
		}
		out = append(out, ex)
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
	if len(out) == 0 {
		tb.Fatal("no examples found")
	}
	return out
}

// frame is a JSON-RPC message with its members kept raw.
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// decodeError decodes an error member the way the SDK does.
func decodeError(raw []byte) (adapterproto.ErrorObject, error) {
	var e adapterproto.ErrorObject
	return e, adapterproto.Decode(raw, &e)
}

// equalJSON reports whether two JSON documents are equal as data.
func equalJSON(a, b []byte) (bool, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya), nil
}
