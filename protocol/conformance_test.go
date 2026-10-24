// Package protocol_test keeps the hand-written JSON Schema of the adapter
// protocol, its golden messages and the Go types in the SDK in agreement. The
// schema and the golden messages are the source of truth for other languages;
// these tests are what stops the Go side from drifting from them.
package protocol_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
	"github.com/aianyai/sidera/sdk/go/peer"
)

// positive reports whether the example is a message or manifest that must be
// accepted and that encodes back to the same data. The wrappers under open/ are
// accepted too, but drop the members a reader ignores, so they are checked by
// TestOpenExamples instead.
func (e example) positive() bool { return e.Kind != "invalid" && e.Kind != "open" }

// TestGoldenMessagesValid: every positive example is accepted by the schemas.
func TestGoldenMessagesValid(t *testing.T) {
	methods := methodByName(t)
	count := map[string]int{}
	for _, ex := range loadExamples(t) {
		if !ex.positive() {
			continue
		}
		t.Run(ex.Path, func(t *testing.T) {
			method := ""
			switch ex.Kind {
			case "manifest", "error":
			case "request", "result", "notification":
				sm, ok := methods[methodOfDir(t, ex.Dir)]
				if !ok {
					t.Fatalf("directory %q is not a method", ex.Dir)
				}
				method = sm.Name
				if ex.Kind == "notification" && !sm.Notification || ex.Kind != "notification" && sm.Notification {
					t.Fatalf("a %s example for %s, which is a %s", ex.Kind, sm.Name, map[bool]string{true: "notification", false: "request"}[sm.Notification])
				}
			default:
				t.Fatalf("unknown example kind %q", ex.Kind)
			}
			if err := schemaVerdict(t, ex.Kind, method, ex.Raw); err != nil {
				t.Fatalf("the schema rejects %s: %v", ex.Path, err)
			}
			if method != "" && ex.Kind != "result" {
				var f frame
				if err := json.Unmarshal(ex.Raw, &f); err != nil || f.Method != method {
					t.Fatalf("example is in the directory of %s but carries method %q", method, f.Method)
				}
			}
			count[ex.Kind]++
		})
	}
	if count["request"] == 0 || count["result"] == 0 || count["error"] == 0 || count["notification"] == 0 || count["manifest"] == 0 {
		t.Fatalf("an example kind is missing: %v", count)
	}
}

func methodOfDir(tb testing.TB, dir string) string {
	tb.Helper()
	for _, sm := range schemaMethods(tb) {
		if dirOf(sm.Name) == dir {
			return sm.Name
		}
	}
	return dir
}

// decodedExample is a positive example decoded into its Go value.
type decodedExample struct {
	example
	value any
}

// decodePositive decodes every positive example into the Go type of its method
// and checks the value encodes back to the same data.
func decodePositive(t *testing.T) []decodedExample {
	t.Helper()
	var out []decodedExample
	for _, ex := range loadExamples(t) {
		if !ex.positive() {
			continue
		}
		var f frame
		method := ""
		if ex.Kind != "manifest" {
			if err := json.Unmarshal(ex.Raw, &f); err != nil {
				t.Fatalf("%s: %v", ex.Path, err)
			}
			method = f.Method
			if ex.Kind == "result" {
				method = methodOfDir(t, ex.Dir)
			}
		}
		var (
			value   any
			encoded []byte
			orig    []byte
			err     error
		)
		switch ex.Kind {
		case "manifest":
			var m manifest.Manifest
			m, err = manifest.Parse(ex.Raw)
			value, orig = m, ex.Raw
			if err == nil {
				encoded, err = json.Marshal(m)
			}
		case "error":
			var e adapterproto.ErrorObject
			e, err = decodeError(f.Error)
			value, orig = e, f.Error
			if err == nil {
				encoded, err = json.Marshal(e)
			}
		default:
			info, ok := adapterproto.Lookup(method)
			if !ok {
				t.Fatalf("%s: no Go method %q", ex.Path, method)
			}
			var v adapterproto.Message
			if ex.Kind == "result" {
				v, err = info.DecodeResult(f.Result)
				orig = f.Result
			} else {
				v, err = info.DecodeParams(f.Params)
				orig = f.Params
			}
			value = v
			if err == nil {
				encoded, err = adapterproto.Encode(v)
			}
		}
		if err != nil {
			t.Errorf("%s: the Go side rejects a golden message: %v", ex.Path, err)
			continue
		}
		same, err := equalJSON(orig, encoded)
		if err != nil || !same {
			t.Errorf("%s: round trip changed the message\n  was: %s\n  now: %s", ex.Path, compact(orig), compact(encoded))
			continue
		}
		out = append(out, decodedExample{ex, value})
	}
	return out
}

func compact(b []byte) string {
	var x any
	if json.Unmarshal(b, &x) != nil {
		return string(b)
	}
	out, _ := json.Marshal(x)
	return string(out)
}

// TestGoldenMessagesRoundTrip: every positive example decodes strictly into the
// Go type of its method, passes the Go validation and encodes back to the same
// data.
func TestGoldenMessagesRoundTrip(t *testing.T) {
	if got := decodePositive(t); len(got) == 0 {
		t.Fatal("nothing decoded")
	}
}

// TestSchemaMethodsMatchGo: the methods, directions and kinds the schema lists are
// exactly the ones Go lists, and every method has the examples it needs.
func TestSchemaMethodsMatchGo(t *testing.T) {
	fromSchema := schemaMethods(t)
	goMethods := adapterproto.Methods()
	if len(fromSchema) != len(goMethods) {
		t.Fatalf("schema has %d methods, Go has %d", len(fromSchema), len(goMethods))
	}
	byName := map[string]adapterproto.MethodInfo{}
	for _, m := range goMethods {
		byName[m.Name] = m
	}
	for _, sm := range fromSchema {
		m, ok := byName[sm.Name]
		if !ok {
			t.Errorf("method %s is in the schema, not in Go", sm.Name)
			continue
		}
		if string(m.Direction) != sm.Direction {
			t.Errorf("%s: schema direction %q, Go %q", sm.Name, sm.Direction, m.Direction)
		}
		if m.Notification != sm.Notification {
			t.Errorf("%s: schema notification=%v, Go %v", sm.Name, sm.Notification, m.Notification)
		}
		if (m.NewResult() == nil) != sm.Notification {
			t.Errorf("%s: a notification has no result type, a request has one", sm.Name)
		}
	}
	// The constant names are the method names the schema uses.
	consts := []string{
		adapterproto.MethodInitialize, adapterproto.MethodPing, adapterproto.MethodCancelRequest, adapterproto.MethodShutdown,
		adapterproto.MethodAdapterStart, adapterproto.MethodAdapterProbe, adapterproto.MethodAdapterAvailability,
		adapterproto.MethodSessionCreate, adapterproto.MethodCheckpointPut, adapterproto.MethodPromptDeliver,
		adapterproto.MethodPromptPersisted, adapterproto.MethodTurnStarted, adapterproto.MethodTurnEnded,
		adapterproto.MethodSessionInterrupt, adapterproto.MethodSessionClose, adapterproto.MethodPromptReconcile,
		adapterproto.MethodRecvObserved, adapterproto.MethodNativeRequestHandled, adapterproto.MethodEndpointRegister,
		adapterproto.MethodEndpointUnregister, adapterproto.MethodEndpointPending, adapterproto.MethodEndpointRecv,
		adapterproto.MethodEndpointAck, adapterproto.MethodEndpointSend, adapterproto.MethodEndpointMessageStatus,
	}
	if len(consts) != len(fromSchema) {
		t.Errorf("%d method constants, %d methods", len(consts), len(fromSchema))
	}
	for _, c := range consts {
		if _, ok := byName[c]; !ok {
			t.Errorf("constant %q is not a method", c)
		}
	}

	have := map[string]map[string]int{}
	for _, ex := range loadExamples(t) {
		if have[ex.Dir] == nil {
			have[ex.Dir] = map[string]int{}
		}
		have[ex.Dir][ex.Kind]++
	}
	for _, sm := range fromSchema {
		d := dirOf(sm.Name)
		if sm.Notification {
			if have[d]["notification"] == 0 {
				t.Errorf("%s has no notification example", sm.Name)
			}
			continue
		}
		if have[d]["request"] == 0 || have[d]["result"] == 0 {
			t.Errorf("%s needs a request and a result example, has %v", sm.Name, have[d])
		}
	}
}

// TestSchemaEnumsMatchGo: every enum of the schema has the values of the Go enum
// of the same name, and every Go enum is in the schema.
func TestSchemaEnumsMatchGo(t *testing.T) {
	defs := sub(t, rawSchema(t, "types"), "$defs")
	goEnums := adapterproto.EnumTables()
	schemaEnums := map[string][]string{}
	for name, d := range defs {
		if e, ok := d.(map[string]any)["enum"].([]any); ok {
			for _, v := range e {
				schemaEnums[name] = append(schemaEnums[name], v.(string))
			}
		}
	}
	for name, want := range goEnums {
		got, ok := schemaEnums[name]
		if !ok {
			t.Errorf("Go enum %s is not defined in the schema", name)
			continue
		}
		if !sameSet(got, want) {
			t.Errorf("enum %s: schema %v, Go %v", name, sorted(got), sorted(want))
		}
	}
	for name := range schemaEnums {
		if _, ok := goEnums[name]; !ok {
			t.Errorf("schema enum %s has no Go enum", name)
		}
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool { return reflect.DeepEqual(sorted(a), sorted(b)) }

// controlEnum lists the values a Go control enum accepts, in code order.
func controlEnum(t reflect.Type) []string {
	var out []string
	for i := 1; i < 256; i++ {
		v := reflect.New(t).Elem()
		v.SetUint(uint64(i))
		if v.Interface().(interface{ Valid() bool }).Valid() {
			out = append(out, v.Interface().(fmt.Stringer).String())
		}
	}
	return out
}

var controlEnums = map[string]reflect.Type{
	"ControlCreateOutcome":    reflect.TypeFor[adapterproto.ControlCreateOutcome](),
	"ControlCreateReason":     reflect.TypeFor[adapterproto.ControlCreateReason](),
	"ControlCloseOutcome":     reflect.TypeFor[adapterproto.ControlCloseOutcome](),
	"ControlCloseReason":      reflect.TypeFor[adapterproto.ControlCloseReason](),
	"ControlInterruptOutcome": reflect.TypeFor[adapterproto.ControlInterruptOutcome](),
	"ControlInterruptReason":  reflect.TypeFor[adapterproto.ControlInterruptReason](),
	"ControlActivity":         reflect.TypeFor[adapterproto.ControlActivity](),
	"ControlCondition":        reflect.TypeFor[adapterproto.ControlCondition](),
}

// TestSchemaControlReplyMatchesGo: the enums written inline in the control reply
// definition are the string tables of the Go control enums.
func TestSchemaControlReplyMatchesGo(t *testing.T) {
	branches := sub(t, rawSchema(t, "types"), "$defs", "ControlReply")["oneOf"].([]any)
	enumAt := func(branch int, prop string) []string {
		p := branches[branch].(map[string]any)["properties"].(map[string]any)[prop].(map[string]any)
		if items, ok := p["items"].(map[string]any); ok {
			p = items
		}
		var out []string
		for _, v := range p["enum"].([]any) {
			out = append(out, v.(string))
		}
		return out
	}
	want := []struct {
		branch int
		prop   string
		goEnum string
	}{
		{0, "outcome", "ControlCreateOutcome"}, {0, "reason", "ControlCreateReason"},
		{1, "outcome", "ControlCloseOutcome"}, {1, "reason", "ControlCloseReason"},
		{2, "outcome", "ControlInterruptOutcome"}, {2, "reason", "ControlInterruptReason"},
		{3, "activity", "ControlActivity"}, {3, "conditions", "ControlCondition"},
	}
	for _, w := range want {
		got, exp := enumAt(w.branch, w.prop), controlEnum(controlEnums[w.goEnum])
		if !sameSet(got, exp) {
			t.Errorf("%s: schema %v, Go %v", w.goEnum, sorted(got), sorted(exp))
		}
	}
	// The codes are the numbers a binary encoding will use: 1-based and dense.
	for name, typ := range controlEnums {
		names := controlEnum(typ)
		for i, n := range names {
			v := reflect.New(typ).Elem()
			v.SetUint(uint64(i + 1))
			if v.Interface().(fmt.Stringer).String() != n {
				t.Errorf("%s: code %d is not the %dth name", name, i+1, i+1)
			}
		}
	}
	signals := rawSchema(t, "types")["$defs"].(map[string]any)["ControlSignal"].(map[string]any)["enum"].([]any)
	for i, b := range branches {
		if got := b.(map[string]any)["title"]; got != signals[i] {
			t.Errorf("control reply branch %d is for %v, the signal list says %v", i, got, signals[i])
		}
	}
}

// TestSchemaErrorCodesMatchGo: the error codes and their names.
func TestSchemaErrorCodesMatchGo(t *testing.T) {
	list := sub(t, rawSchema(t, "message"), "$defs", "ErrorCode")["oneOf"].([]any)
	got := map[int]string{}
	for _, e := range list {
		m := e.(map[string]any)
		got[int(m["const"].(float64))] = m["title"].(string)
	}
	// The codes of a frame are the peer's and the application codes are the
	// message types': together they are the schema's list, each in one place.
	want := map[int]string{}
	for _, code := range peer.Codes() {
		want[code] = peer.CodeName(code)
	}
	for _, c := range adapterproto.ApplicationCodes() {
		if _, dup := want[c.Code]; dup {
			t.Errorf("code %d is defined by both the peer and adapterproto", c.Code)
		}
		want[c.Code] = c.Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("error codes differ:\n schema %v\n Go     %v", got, want)
	}
	// Written out independently: the JSON-RPC codes and the protocol's own.
	independent := map[int]string{
		-32700: "parse_error", -32600: "invalid_request", -32601: "method_not_found", -32602: "invalid_params", -32603: "internal_error",
		-32001: "overloaded", -32002: "shutting_down", -32003: "not_initialized", -32004: "line_too_long", -32022: "unsupported_protocol_version",
		-32800: "request_cancelled", 1001: "unknown_endpoint", 1002: "wrong_endpoint_kind", 1003: "capability_not_declared",
		1004: "unknown_attempt", 1005: "attempt_conflict", 1006: "checkpoint_too_large", 1007: "endpoint_limit", 1008: "invalid_address",
		1009: "send_refused", 1010: "lease_unknown", 1011: "instance_locked",
	}
	if !reflect.DeepEqual(want, independent) {
		t.Errorf("Go error codes differ from the expected table:\n Go       %v\n expected %v", want, independent)
	}
	for constant, value := range map[int]int{
		peer.CodeParseError: -32700, peer.CodeInvalidRequest: -32600, peer.CodeMethodNotFound: -32601, peer.CodeInvalidParams: -32602,
		peer.CodeInternalError: -32603, peer.CodeOverloaded: -32001, peer.CodeShuttingDown: -32002, peer.CodeNotInitialized: -32003,
		peer.CodeLineTooLong: -32004, peer.CodeUnsupportedProtocolVersion: -32022, peer.CodeRequestCancelled: -32800,
		adapterproto.CodeUnknownEndpoint: 1001, adapterproto.CodeWrongEndpointKind: 1002, adapterproto.CodeCapabilityNotDeclared: 1003,
		adapterproto.CodeUnknownAttempt: 1004, adapterproto.CodeAttemptConflict: 1005, adapterproto.CodeCheckpointTooLarge: 1006,
		adapterproto.CodeEndpointLimit: 1007, adapterproto.CodeInvalidAddress: 1008, adapterproto.CodeSendRefused: 1009,
		adapterproto.CodeLeaseUnknown: 1010, adapterproto.CodeInstanceLocked: 1011,
	} {
		if constant != value {
			t.Errorf("a code constant is %d, want %d", constant, value)
		}
	}
}

// TestSchemaPatternsMatchGo: the patterns of the schema are the Go patterns.
func TestSchemaPatternsMatchGo(t *testing.T) {
	defs := sub(t, rawSchema(t, "types"), "$defs")
	for name, want := range adapterproto.Patterns() {
		d, ok := defs[name].(map[string]any)
		if !ok {
			t.Errorf("pattern %s is not defined in the schema", name)
			continue
		}
		if got, _ := d["pattern"].(string); got != want {
			t.Errorf("pattern %s:\n schema %s\n Go     %s", name, got, want)
		}
	}
	for name, d := range defs {
		if _, has := d.(map[string]any)["pattern"]; has {
			if _, ok := adapterproto.Patterns()[name]; !ok {
				t.Errorf("schema pattern %s is not in Go", name)
			}
		}
	}
}

// TestPatternsRefuseATrailingNewline: every pattern ends with $, which some
// regular-expression engines let match before a trailing newline. Neither the
// schema validator nor Go may accept a value that differs from a valid one by a
// newline at its end; the README tells other implementations to anchor fully.
func TestPatternsRefuseATrailingNewline(t *testing.T) {
	valid := map[string]string{
		"AttemptID":  "6f1c2a4e-8b3d-4c5a-9e7f-0a1b2c3d4e5f",
		"LeaseRef":   "0123456789abcdef0123456789abcdef",
		"Token":      "k3JpQ9vL2mXwR7tYbN5cZ8dHfG1sAeUo0iWxM4yTqBA",
		"AdapterID":  "example",
		"PresetName": "review.readonly",
		"ConfigKey":  "project_dir",
		"EnvName":    "SIDERA_ENDPOINT_TOKEN",
		"ReasonWord": "too_many_recipients",
		"SemVer":     "2.1.307-rc.1+build.5",
	}
	s := schemas(t)
	for name, pattern := range adapterproto.Patterns() {
		sample, ok := valid[name]
		if !ok {
			t.Errorf("no valid sample for pattern %s", name)
			continue
		}
		re := regexp.MustCompile(pattern)
		ref := "types.schema.json#/$defs/" + name
		if !re.MatchString(sample) || s.accepts(t, ref, sample) != nil {
			t.Errorf("%s: the sample %q is not valid", name, sample)
			continue
		}
		for _, bad := range []string{sample + "\n", sample + "\r\n", "\n" + sample} {
			if re.MatchString(bad) {
				t.Errorf("%s: the Go pattern matches %q", name, bad)
			}
			if s.accepts(t, ref, bad) == nil {
				t.Errorf("%s: the schema accepts %q", name, bad)
			}
		}
	}
	for name, err := range map[string]error{
		"ValidatePresetName": adapterproto.ValidatePresetName("default\n"),
		"ValidateAdapterID":  adapterproto.ValidateAdapterID("example\n"),
		"ValidateConfigKey":  adapterproto.ValidateConfigKey("project_dir\n"),
		"ValidateSemVer":     adapterproto.ValidateSemVer("1.2.3\n"),
	} {
		if err == nil {
			t.Errorf("%s accepts a value with a trailing newline", name)
		}
	}
}

// TestManifestVocabulariesMatchSchema: the manifest's enums and the platform
// vocabularies.
func TestManifestVocabulariesMatchSchema(t *testing.T) {
	raw := rawSchema(t, "manifest")
	list := func(v any) []string {
		var out []string
		for _, e := range v.([]any) {
			out = append(out, e.(string))
		}
		return out
	}
	defs := sub(t, raw, "$defs")
	pairs := map[string][]string{
		"GOOS":      manifest.KnownOS(),
		"GOARCH":    manifest.KnownArch(),
		"FieldType": toStrings(manifest.FieldTypes()),
	}
	for name, want := range pairs {
		if got := list(defs[name].(map[string]any)["enum"]); !sameSet(got, want) {
			t.Errorf("%s: schema %v, Go %v", name, sorted(got), sorted(want))
		}
	}
	item := sub(t, raw, "properties", "budgets", "items", "properties")
	if got := list(item["frontend"].(map[string]any)["enum"]); !sameSet(got, toStrings(manifest.BudgetFrontends())) {
		t.Errorf("budget frontends: schema %v", got)
	}
	if got := list(item["unit"].(map[string]any)["enum"]); !sameSet(got, toStrings(manifest.Units())) {
		t.Errorf("budget units: schema %v", got)
	}
	if got := sub(t, raw, "properties", "budgets", "items", "properties", "limit")["maximum"]; got != float64(manifest.MaxBudgetLimit) {
		t.Errorf("budget limit maximum: schema %v, Go %d", got, manifest.MaxBudgetLimit)
	}
}

// TestManifestRecvWaitLimitsMatchSchema: the front ends a receive wait limit can
// name and the bounds of its seconds are the schema's.
func TestManifestRecvWaitLimitsMatchSchema(t *testing.T) {
	raw := rawSchema(t, "manifest")
	limits := sub(t, raw, "properties", "recv_wait_limits")
	if got := limits["minItems"]; got != float64(1) {
		t.Errorf("recv_wait_limits minItems: schema %v, want 1", got)
	}
	if got := limits["maxItems"]; got != float64(len(manifest.BudgetFrontends())) {
		t.Errorf("recv_wait_limits maxItems: schema %v, want one per front end (%d)", got, len(manifest.BudgetFrontends()))
	}
	item := sub(t, raw, "properties", "recv_wait_limits", "items")
	if got := item["required"]; !sameSet(anyStrings(t, got), []string{"frontend", "max_s"}) {
		t.Errorf("recv_wait_limits required members: schema %v", got)
	}
	props := sub(t, raw, "properties", "recv_wait_limits", "items", "properties")
	if got := anyStrings(t, props["frontend"].(map[string]any)["enum"]); !sameSet(got, toStrings(manifest.BudgetFrontends())) {
		t.Errorf("recv_wait_limits front ends: schema %v", got)
	}
	maxS := props["max_s"].(map[string]any)
	if maxS["type"] != "integer" || maxS["minimum"] != float64(1) || maxS["maximum"] != float64(manifest.MaxRecvWaitSeconds) {
		t.Errorf("recv_wait_limits max_s: schema %v, want an integer in 1..%d", maxS, manifest.MaxRecvWaitSeconds)
	}
}

func anyStrings(tb testing.TB, v any) []string {
	tb.Helper()
	list, ok := v.([]any)
	if !ok {
		tb.Fatalf("not a list: %v", v)
	}
	out := make([]string, len(list))
	for i, e := range list {
		out[i], _ = e.(string)
	}
	return out
}

func toStrings[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

// TestPlatformVocabularyCoversToolchain: every operating system and architecture
// the Go toolchain builds for is in the vocabulary. Skipped when the go command
// cannot be run.
func TestPlatformVocabularyCoversToolchain(t *testing.T) {
	out, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		t.Skipf("go tool dist list: %v", err)
	}
	oses, arches := map[string]bool{}, map[string]bool{}
	for _, o := range manifest.KnownOS() {
		oses[o] = true
	}
	for _, a := range manifest.KnownArch() {
		arches[a] = true
	}
	n := 0
	for _, pair := range strings.Fields(string(out)) {
		os, arch, _ := strings.Cut(pair, "/")
		n++
		if !oses[os] || !arches[arch] {
			t.Errorf("the toolchain builds for %s, outside the vocabulary", pair)
		}
	}
	if n < 20 {
		t.Fatalf("only %d platforms listed", n)
	}
}

// goEnumValues walks a decoded value and records, per enum type, the values it
// holds.
func goEnumValues(v reflect.Value, seen map[string]map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			goEnumValues(v.Elem(), seen)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				goEnumValues(v.Field(i), seen)
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			goEnumValues(v.Index(i), seen)
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			goEnumValues(v.MapIndex(k), seen)
		}
	case reflect.String:
		record(v, v.String(), seen)
	case reflect.Uint8:
		if s, ok := v.Interface().(fmt.Stringer); ok {
			record(v, s.String(), seen)
		}
	}
}

func record(v reflect.Value, text string, seen map[string]map[string]bool) {
	t := v.Type()
	if t.Name() == "" || t.PkgPath() == "" || t.PkgPath() == "builtin" {
		return
	}
	key := t.Name()
	if strings.HasSuffix(t.PkgPath(), "/manifest") {
		key = "manifest." + key
	} else if !strings.HasSuffix(t.PkgPath(), "/adapterproto") {
		return
	}
	if seen[key] == nil {
		seen[key] = map[string]bool{}
	}
	seen[key][text] = true
}

// TestEveryEnumValueAppearsInAnExample: each value of each enum appears, as a
// value of that enum, in at least one golden message.
func TestEveryEnumValueAppearsInAnExample(t *testing.T) {
	seen := map[string]map[string]bool{}
	for _, d := range decodePositive(t) {
		goEnumValues(reflect.ValueOf(d.value), seen)
	}
	expect := map[string][]string{}
	for name, vals := range adapterproto.EnumTables() {
		expect[name] = vals
	}
	for name, typ := range controlEnums {
		expect[name] = controlEnum(typ)
	}
	expect["manifest.FieldType"] = toStrings(manifest.FieldTypes())
	expect["manifest.Frontend"] = toStrings(manifest.BudgetFrontends())
	expect["manifest.Unit"] = toStrings(manifest.Units())
	for name, vals := range expect {
		for _, v := range vals {
			if !seen[name][v] {
				t.Errorf("no golden message holds %s = %q", name, v)
			}
		}
	}
}

// TestEveryErrorCodeAppearsInAnExample: each error code has an example.
func TestEveryErrorCodeAppearsInAnExample(t *testing.T) {
	seen := map[int]bool{}
	for _, d := range decodePositive(t) {
		if e, ok := d.value.(adapterproto.ErrorObject); ok {
			seen[e.Code] = true
		}
	}
	for _, code := range peer.Codes() {
		if !seen[code] {
			t.Errorf("no golden error holds code %d (%s)", code, peer.CodeName(code))
		}
	}
	for _, c := range adapterproto.ApplicationCodes() {
		if !seen[c.Code] {
			t.Errorf("no golden error holds code %d (%s)", c.Code, c.Name)
		}
	}
}

// TestInvalidExamples: every invalid example is rejected at the layer it says.
// "schema": the schema rejects it. "go": the schema cannot state the rule, so
// the schema accepts it and the Go validation rejects it with a rule beyond the
// schema. "both": the schema and Go reject it. Whatever the schema rejects, Go
// rejects too, except at frame level, where the peer decodes frames and the
// message types never see them.
func TestInvalidExamples(t *testing.T) {
	counts := map[string]int{}
	seenName := map[string]bool{}
	for _, ex := range loadExamples(t) {
		if ex.Dir != "invalid" {
			continue
		}
		var wrapper struct {
			Expect   string          `json:"expect"`
			Kind     string          `json:"kind"`
			Method   string          `json:"method"`
			Note     string          `json:"note"`
			Document json.RawMessage `json:"document"`
		}
		if err := json.Unmarshal(ex.Raw, &wrapper); err != nil {
			t.Fatalf("%s: %v", ex.Path, err)
		}
		if seenName[wrapper.Note+"|"+wrapper.Kind+"|"+wrapper.Method] {
			t.Errorf("%s: duplicate case %q", ex.Path, wrapper.Note)
		}
		seenName[wrapper.Note+"|"+wrapper.Kind+"|"+wrapper.Method] = true
		counts[wrapper.Expect]++
		t.Run(ex.Name, func(t *testing.T) {
			schemaErr := schemaVerdict(t, wrapper.Kind, wrapper.Method, wrapper.Document)
			_, goErr := goVerdict(wrapper.Kind, wrapper.Method, wrapper.Document)
			switch wrapper.Expect {
			case "both":
				if schemaErr == nil {
					t.Errorf("%s (%s): the schema accepts it", ex.Name, wrapper.Note)
				}
				if goErr == nil {
					t.Errorf("%s (%s): Go accepts it", ex.Name, wrapper.Note)
				}
			case "schema":
				if schemaErr == nil {
					t.Errorf("%s (%s): the schema accepts it", ex.Name, wrapper.Note)
				}
				if wrapper.Kind != "frame" && wrapper.Kind != "message" && goErr == nil {
					t.Errorf("%s (%s): Go is looser than the schema", ex.Name, wrapper.Note)
				}
			case "go":
				if schemaErr != nil {
					t.Errorf("%s (%s): the schema rejects it, so expect should not be \"go\": %v", ex.Name, wrapper.Note, schemaErr)
				}
				if goErr == nil {
					t.Errorf("%s (%s): Go accepts it", ex.Name, wrapper.Note)
				} else if !errors.Is(goErr, adapterproto.ErrBeyondSchema) {
					t.Errorf("%s (%s): Go rejects it for a rule the schema could state: %v", ex.Name, wrapper.Note, goErr)
				}
			default:
				t.Errorf("%s: unknown expect %q", ex.Name, wrapper.Expect)
			}
		})
	}
	if counts["both"] < 100 || counts["schema"] == 0 || counts["go"] < 10 {
		t.Errorf("too few invalid cases: %v", counts)
	}
}

// TestOpenExamples: a document with members that a reader of this version does
// not know, in the places the protocol leaves open, is accepted by the schema and
// by Go, and Go reads it as the same document without those members.
func TestOpenExamples(t *testing.T) {
	n := 0
	for _, ex := range loadExamples(t) {
		if ex.Dir != "open" {
			continue
		}
		n++
		var wrapper struct {
			Kind     string          `json:"kind"`
			Method   string          `json:"method"`
			Note     string          `json:"note"`
			Ignoring []string        `json:"ignoring"`
			Document json.RawMessage `json:"document"`
		}
		if err := json.Unmarshal(ex.Raw, &wrapper); err != nil {
			t.Fatalf("%s: %v", ex.Path, err)
		}
		t.Run(ex.Name, func(t *testing.T) {
			if len(wrapper.Ignoring) == 0 {
				t.Fatal("an open example names the members that are ignored")
			}
			if err := schemaVerdict(t, wrapper.Kind, wrapper.Method, wrapper.Document); err != nil {
				t.Fatalf("the schema rejects it: %v", err)
			}
			value, err := goVerdict(wrapper.Kind, wrapper.Method, wrapper.Document)
			if err != nil {
				t.Fatalf("Go rejects it: %v", err)
			}
			encoded, err := adapterproto.Encode(value)
			if err != nil {
				t.Fatal(err)
			}
			// What Go read is the document without the ignored members.
			tree, _ := parseTree(t, wrapper.Document)
			for _, ptr := range wrapper.Ignoring {
				steps := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
				p := make(pos, len(steps))
				for i, s := range steps {
					p[i] = s
				}
				if at(tree, p) == nil {
					t.Fatalf("the document has no member %s to ignore", ptr)
				}
				delete(parentOf(tree, p).(map[string]any), steps[len(steps)-1])
			}
			var f frame
			whole, _ := json.Marshal(tree)
			if err := json.Unmarshal(whole, &f); err != nil {
				t.Fatal(err)
			}
			want := f.Params
			if wrapper.Kind == "result" {
				want = f.Result
			}
			if same, err := equalJSON(want, encoded); err != nil || !same {
				t.Fatalf("Go read\n  %s\nwant\n  %s", compact(encoded), compact(want))
			}
		})
	}
	if n < 3 {
		t.Fatalf("only %d open examples", n)
	}
}

// TestMembersAreClosedExceptInCapabilities: an unknown member added to any object
// of an initialize message is refused by the schema and by Go, except in the
// objects that declare capabilities, which both accept it in. This is the whole
// extent of the openness: a new member anywhere else needs a new protocol
// version.
func TestMembersAreClosedExceptInCapabilities(t *testing.T) {
	open := map[string]bool{
		".params.capabilities": true, ".result.capabilities": true,
		".result.capabilities.runtime": true, ".result.capabilities.direct": true,
	}
	var closedChecked, openChecked int
	for _, ex := range loadExamples(t) {
		if ex.Dir != "initialize" || !ex.positive() || ex.Kind == "error" {
			continue
		}
		tree, ok := parseTree(t, ex.Raw)
		if !ok {
			t.Fatal(ex.Path)
		}
		var all []pos
		positions(tree, nil, &all)
		for _, p := range all {
			if _, isObject := at(tree, p).(map[string]any); !isObject {
				continue
			}
			t.Run(ex.Path+p.String(), func(t *testing.T) {
				edited, _ := parseTree(t, ex.Raw)
				at(edited, p).(map[string]any)["x_unknown_member"] = json.Number("1")
				raw, err := json.Marshal(edited)
				if err != nil {
					t.Fatal(err)
				}
				method := ""
				if ex.Kind == "result" {
					method = "initialize"
				}
				schemaErr := schemaVerdict(t, ex.Kind, method, raw)
				_, goErr := goVerdict(ex.Kind, method, raw)
				if open[p.String()] {
					openChecked++
					if schemaErr != nil || goErr != nil {
						t.Errorf("an open object refuses an unknown member: schema %v, Go %v", schemaErr, goErr)
					}
					return
				}
				closedChecked++
				if schemaErr == nil || goErr == nil {
					t.Errorf("a closed object accepts an unknown member: schema %v, Go %v", schemaErr, goErr)
				}
			})
		}
	}
	if closedChecked < 10 || openChecked < 4 {
		t.Errorf("checked %d closed and %d open objects", closedChecked, openChecked)
	}
}

var numberRE = regexp.MustCompile(`^-?[0-9]+$`)

// TestEnumNamesMatchContractPackages: the protocol's delivery states, rejection
// reasons and control signals are the names the daemon's contract packages give
// the same things, so a state needs no translation on its way to the sender.
func TestEnumNamesMatchContractPackages(t *testing.T) {
	var states []string
	for s := delivery.StateQueued; s <= delivery.StateClosedUnconfirmed; s++ {
		states = append(states, s.String())
	}
	var reasons []string
	for r := delivery.RejectUnauthorized; r <= delivery.RejectDeadlineOutOfRange; r++ {
		reasons = append(reasons, r.String())
	}
	signals := []string{authz.ActionSessionCreate.String(), authz.ActionSessionClose.String(), authz.ActionSessionInterrupt.String(), authz.ActionEndpointStatus.String()}
	tables := adapterproto.EnumTables()
	for name, want := range map[string][]string{"DeliveryState": states, "RejectReason": reasons, "ControlSignal": signals} {
		if !sameSet(tables[name], want) {
			t.Errorf("%s: protocol %v, contract package %v", name, sorted(tables[name]), sorted(want))
		}
	}
	if len(states) != 7 || len(reasons) != 10 {
		t.Errorf("the contract packages list %d states and %d reasons; the protocol was written for 7 and 10", len(states), len(reasons))
	}
}

// TestUniqueArrayLimits: the lists that must hold distinct entries cannot be
// resized by repeating one, so their limits are probed with distinct entries.
func TestUniqueArrayLimits(t *testing.T) {
	oses, arches := manifest.KnownOS(), manifest.KnownArch()
	build := func(platforms, options int) []byte {
		var ps []map[string]string
		for _, o := range oses {
			for _, a := range arches {
				if len(ps) < platforms {
					ps = append(ps, map[string]string{"os": o, "arch": a})
				}
			}
		}
		doc := map[string]any{
			"manifest_version": 1, "id": "x", "version": "1.0.0", "protocol": map[string]int{"min": 1, "max": 1},
			"entry": map[string]string{"program": "p"}, "platforms": ps,
		}
		if options > 0 {
			var opts []string
			for i := 0; i < options; i++ {
				opts = append(opts, fmt.Sprintf("option-%d", i))
			}
			doc["config"] = map[string]any{"instance": []map[string]any{{"key": "k", "type": "enum", "title": "T", "options": opts}}}
		}
		b, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, tc := range []struct {
		name              string
		platforms, option int
		ok                bool
	}{
		{"64 platforms", 64, 0, true}, {"65 platforms", 65, 0, false},
		{"64 options", 1, 64, true}, {"65 options", 1, 65, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := build(tc.platforms, tc.option)
			schemaErr := schemaVerdict(t, "manifest", "", raw)
			_, goErr := manifest.Parse(raw)
			if (schemaErr == nil) != tc.ok || (goErr == nil) != tc.ok {
				t.Fatalf("schema: %v, Go: %v, want ok=%v", schemaErr, goErr, tc.ok)
			}
		})
	}
}

// TestMessageShapeMatchesMethodKind: a request method needs an id and a
// notification method must not have one, for every method.
func TestMessageShapeMatchesMethodKind(t *testing.T) {
	checked := map[string]int{}
	for _, ex := range loadExamples(t) {
		if ex.Kind != "request" && ex.Kind != "notification" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(ex.Raw, &doc); err != nil {
			t.Fatal(err)
		}
		if ex.Kind == "request" {
			delete(doc, "id")
		} else {
			doc["id"] = 99
		}
		raw, _ := json.Marshal(doc)
		if err := schemaVerdict(t, "message", "", raw); err == nil {
			t.Errorf("%s: the schema accepts the %s with the wrong shape: %s", ex.Path, ex.Kind, raw)
		}
		checked[doc["method"].(string)]++
	}
	if len(checked) != len(adapterproto.Methods()) {
		t.Errorf("checked %d methods, the protocol has %d", len(checked), len(adapterproto.Methods()))
	}
}

// TestFieldLimits: the lists of configuration fields hold 64 fields with distinct
// keys at most.
func TestFieldLimits(t *testing.T) {
	build := func(group string, n int) []byte {
		var fs []map[string]any
		for i := 0; i < n; i++ {
			fs = append(fs, map[string]any{"key": fmt.Sprintf("k%d", i), "type": "boolean", "title": "T"})
		}
		doc := map[string]any{
			"manifest_version": 1, "id": "x", "version": "1.0.0", "protocol": map[string]int{"min": 1, "max": 1},
			"entry": map[string]string{"program": "p"}, "platforms": []map[string]string{{"os": "linux", "arch": "amd64"}},
			"config": map[string]any{group: fs},
		}
		b, _ := json.Marshal(doc)
		return b
	}
	for _, group := range []string{"instance", "preset"} {
		for _, tc := range []struct {
			n  int
			ok bool
		}{{1, true}, {64, true}, {65, false}} {
			raw := build(group, tc.n)
			schemaErr := schemaVerdict(t, "manifest", "", raw)
			_, goErr := manifest.Parse(raw)
			if (schemaErr == nil) != tc.ok || (goErr == nil) != tc.ok {
				t.Errorf("%d %s fields: schema %v, Go %v, want ok=%v", tc.n, group, schemaErr, goErr, tc.ok)
			}
		}
	}
}
