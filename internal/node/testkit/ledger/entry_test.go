package ledger_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

func ptr[T any](v T) *T { return &v }

// goldenEntries are entries with the exact line each is written as. The lines
// are written out by hand: the format is a contract between the writers (which
// may be separate processes) and every reader of a ledger, so a change to a
// member name, their order or the escaping must show up here.
var goldenEntries = []struct {
	name  string
	entry ledger.Entry
	line  string
}{
	{
		"an adapter start",
		ledger.Entry{V: 1, Writer: "adapter/fake", PID: 0, Seq: 1, AtMS: 1792832400000, Step: "", Kind: ledger.KindAdapterStarted, Attrs: ledger.Attrs{Instance: "fake"}},
		`{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1792832400000,"step":"","kind":"adapter.started","attrs":{"instance":"fake"}}`,
	},
	{
		"a refused instance lock",
		ledger.Entry{V: 1, Writer: "adapter/fake", PID: 4242, Seq: 2, AtMS: 1792832401000, Step: "start", Kind: ledger.KindAdapterLockRefused, Attrs: ledger.Attrs{Instance: "fake", LockPath: "/tmp/s1-x/instance.lock"}},
		`{"v":1,"writer":"adapter/fake","pid":4242,"seq":2,"at_ms":1792832401000,"step":"start","kind":"adapter.lock.refused","attrs":{"instance":"fake","lock_path":"/tmp/s1-x/instance.lock"}}`,
	},
	{
		"a session creation that reached the native runtime",
		ledger.Entry{V: 1, Writer: "adapter/fake", PID: 0, Seq: 3, AtMS: 1792832460000, Step: "create", Kind: ledger.KindNativeCreateReturned,
			Attrs: ledger.Attrs{Attempt: "00000000-0000-4000-8000-000000000001", NativeRef: "n-0123", RuntimeWriter: "runtime/n-0123", RuntimeSeq: 1, Outcome: "created"}},
		`{"v":1,"writer":"adapter/fake","pid":0,"seq":3,"at_ms":1792832460000,"step":"create","kind":"native.create.returned","attrs":{"attempt":"00000000-0000-4000-8000-000000000001","native_ref":"n-0123","runtime_writer":"runtime/n-0123","runtime_seq":1,"outcome":"created"}}`,
	},
	{
		"a session creation that failed before any native session existed names no runtime entry",
		ledger.Entry{V: 1, Writer: "adapter/fake", PID: 0, Seq: 4, AtMS: 1792832460000, Step: "create", Kind: ledger.KindNativeCreateReturned,
			Attrs: ledger.Attrs{Attempt: "00000000-0000-4000-8000-000000000002", Outcome: "failed"}},
		`{"v":1,"writer":"adapter/fake","pid":0,"seq":4,"at_ms":1792832460000,"step":"create","kind":"native.create.returned","attrs":{"attempt":"00000000-0000-4000-8000-000000000002","outcome":"failed"}}`,
	},
	{
		"a message the model saw a second time",
		ledger.Entry{V: 1, Writer: "runtime/n-0123", PID: 0, Seq: 7, AtMS: 1792832520000, Step: "recv again", Kind: ledger.KindModelSaw,
			Attrs: ledger.Attrs{Endpoint: "sess-1", MessageID: "m1", Part: 1, Parts: 2, PossiblyDuplicate: ptr(true), BodyDigest: "ab", From: "alice/agent"}},
		`{"v":1,"writer":"runtime/n-0123","pid":0,"seq":7,"at_ms":1792832520000,"step":"recv again","kind":"model.saw","attrs":{"endpoint":"sess-1","message_id":"m1","part":1,"parts":2,"possibly_duplicate":true,"body_digest":"ab","from":"alice/agent"}}`,
	},
	{
		"a message the model saw once, with the duplicate mark explicitly false",
		ledger.Entry{V: 1, Writer: "runtime/n-0123", PID: 0, Seq: 8, AtMS: 1792832520000, Step: "", Kind: ledger.KindModelSaw,
			Attrs: ledger.Attrs{Endpoint: "sess-1", MessageID: "m2", PossiblyDuplicate: ptr(false), BodyDigest: "cd", From: "alice/agent"}},
		`{"v":1,"writer":"runtime/n-0123","pid":0,"seq":8,"at_ms":1792832520000,"step":"","kind":"model.saw","attrs":{"endpoint":"sess-1","message_id":"m2","possibly_duplicate":false,"body_digest":"cd","from":"alice/agent"}}`,
	},
	{
		"a client call with an idempotency key",
		ledger.Entry{V: 1, Writer: "agent/sess-1", PID: 0, Seq: 1, AtMS: 1792832400000, Step: "a sends", Kind: ledger.KindClientCall,
			Attrs: ledger.Attrs{Action: "send", IdemKey: "k1", Endpoint: "sess-1"}},
		`{"v":1,"writer":"agent/sess-1","pid":0,"seq":1,"at_ms":1792832400000,"step":"a sends","kind":"client.call","attrs":{"endpoint":"sess-1","action":"send","idem_key":"k1"}}`,
	},
	{
		"a client result",
		ledger.Entry{V: 1, Writer: "agent/sess-1", PID: 0, Seq: 2, AtMS: 1792832400000, Step: "a sends", Kind: ledger.KindClientResult,
			Attrs: ledger.Attrs{Action: "send", IdemKey: "k1", Category: "ok", MessageID: "m1"}},
		`{"v":1,"writer":"agent/sess-1","pid":0,"seq":2,"at_ms":1792832400000,"step":"a sends","kind":"client.result","attrs":{"message_id":"m1","action":"send","idem_key":"k1","category":"ok"}}`,
	},
	{
		"a knob hit",
		ledger.Entry{V: 1, Writer: "adapter/fake", PID: 0, Seq: 4, AtMS: 1792832400000, Step: "", Kind: ledger.KindKnobHit, Attrs: ledger.Attrs{Knob: "create.hold", Detail: "attempt 1"}},
		`{"v":1,"writer":"adapter/fake","pid":0,"seq":4,"at_ms":1792832400000,"step":"","kind":"knob.hit","attrs":{"knob":"create.hold","detail":"attempt 1"}}`,
	},
	{
		"a killed process list",
		ledger.Entry{V: 1, Writer: "fixture", PID: 0, Seq: 5, AtMS: 1792832400000, Step: "kill", Kind: ledger.KindFixtureKill, Attrs: ledger.Attrs{PIDs: []int64{4242, 4243}}},
		`{"v":1,"writer":"fixture","pid":0,"seq":5,"at_ms":1792832400000,"step":"kill","kind":"fixture.kill","attrs":{"pids":[4242,4243]}}`,
	},
	{
		"a runtime interruption has no attributes",
		ledger.Entry{V: 1, Writer: "runtime/n-0123", PID: 0, Seq: 9, AtMS: 1792832400000, Step: "", Kind: ledger.KindRuntimeInterrupted},
		`{"v":1,"writer":"runtime/n-0123","pid":0,"seq":9,"at_ms":1792832400000,"step":"","kind":"runtime.interrupted","attrs":{}}`,
	},
	{
		"text is escaped as JSON and the characters < > & are not",
		ledger.Entry{V: 1, Writer: "fixture", PID: 0, Seq: 6, AtMS: 1792832400000, Step: "a<b>&\"c\"\n", Kind: ledger.KindFixtureStep, Attrs: ledger.Attrs{Name: "a<b>&\"c\"\n"}},
		`{"v":1,"writer":"fixture","pid":0,"seq":6,"at_ms":1792832400000,"step":"a<b>&\"c\"\n","kind":"fixture.step","attrs":{"name":"a<b>&\"c\"\n"}}`,
	},
}

// TestGoldenEntryVectors: each entry encodes to its hand-written line, and the
// line decodes to the entry.
func TestGoldenEntryVectors(t *testing.T) {
	for _, g := range goldenEntries {
		t.Run(g.name, func(t *testing.T) {
			got, err := ledger.Encode(g.entry)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if string(got) != g.line {
				t.Fatalf("Encode:\n got %s\nwant %s", got, g.line)
			}
			back, err := ledger.Decode([]byte(g.line))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(back, g.entry) {
				t.Fatalf("Decode:\n got %+v\nwant %+v", back, g.entry)
			}
		})
	}
}

// base is a valid line the rejection rows start from.
const base = `{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1792832400000,"step":"","kind":"adapter.started","attrs":{"instance":"fake"}}`

// TestDecodeRejects: one row per rule a reader must enforce. A ledger is the
// oracle of the integration tests, so a line that does not mean exactly one
// thing must not be read at all.
func TestDecodeRejects(t *testing.T) {
	rows := []struct {
		rule string
		line string
	}{
		{"an unknown member is an error", strings.Replace(base, `"step":""`, `"step":"","extra":1`, 1)},
		{"a missing member is an error", strings.Replace(base, `"pid":0,`, ``, 1)},
		{"the version must be 1", strings.Replace(base, `"v":1`, `"v":2`, 1)},
		{"null is not a value", strings.Replace(base, `"step":""`, `"step":null`, 1)},
		{"a repeated member is an error", strings.Replace(base, `"seq":1`, `"seq":1,"seq":1`, 1)},
		{"a member name in another case is an unknown member", strings.Replace(base, `"writer"`, `"Writer"`, 1)},
		{"members out of their order are not the canonical line", `{"writer":"adapter/fake","v":1,"pid":0,"seq":1,"at_ms":1792832400000,"step":"","kind":"adapter.started","attrs":{"instance":"fake"}}`},
		{"white space is not the canonical line", strings.Replace(base, `"v":1,`, `"v": 1,`, 1)},
		{"a trailing newline is not part of the line", base + "\n"},
		{"data after the object is an error", base + " {}"},
		{"an unknown kind is an error", strings.Replace(base, `adapter.started`, `adapter.sulked`, 1)},
		{"an attribute the kind does not take is an error", strings.Replace(base, `"instance":"fake"`, `"instance":"fake","lease":"00"`, 1)},
		{"an attribute the kind requires is an error when missing", strings.Replace(base, `"instance":"fake"`, ``, 1)},
		{"an unknown attribute is an error", strings.Replace(base, `"instance":"fake"`, `"instance":"fake","mood":"good"`, 1)},
		{"a writer name outside the vocabulary is an error", strings.Replace(base, `adapter/fake`, `daemon`, 1)},
		{"a writer with an empty name is an error", strings.Replace(base, `adapter/fake`, `adapter/`, 1)},
		{"the sequence starts at 1", strings.Replace(base, `"seq":1`, `"seq":0`, 1)},
		{"the sequence is not negative", strings.Replace(base, `"seq":1`, `"seq":-1`, 1)},
		{"a time is not negative", strings.Replace(base, `1792832400000`, `-1`, 1)},
		{"a pid is not negative", strings.Replace(base, `"pid":0`, `"pid":-1`, 1)},
		{"a fraction is not an integer", strings.Replace(base, `"seq":1`, `"seq":1.5`, 1)},
		{"a string is not a number", strings.Replace(base, `"seq":1`, `"seq":"1"`, 1)},
		{"invalid UTF-8 is an error", strings.Replace(base, `"step":""`, "\"step\":\"\xff\"", 1)},
		{"an empty attribute value is an error: omit it", strings.Replace(base, `"instance":"fake"`, `"instance":""`, 1)},
		{"an empty line is an error", ``},
		{"a creation that names a native session must name the runtime's entry", `{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1,"step":"","kind":"native.create.returned","attrs":{"attempt":"a","native_ref":"n-0123","outcome":"created"}}`},
		{"a runtime entry without a native session has nothing to point at", `{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1,"step":"","kind":"native.create.returned","attrs":{"attempt":"a","runtime_writer":"runtime/n-0123","runtime_seq":1,"outcome":"failed"}}`},
		{"the runtime's writer and sequence number go together", `{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1,"step":"","kind":"native.create.returned","attrs":{"attempt":"a","native_ref":"n-0123","runtime_writer":"runtime/n-0123","outcome":"created"}}`},
		{"the named writer is a runtime", `{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1,"step":"","kind":"native.create.returned","attrs":{"attempt":"a","native_ref":"n-0123","runtime_writer":"adapter/fake","runtime_seq":1,"outcome":"created"}}`},
		{"the runtime's sequence number starts at 1", `{"v":1,"writer":"adapter/fake","pid":0,"seq":1,"at_ms":1,"step":"","kind":"native.create.returned","attrs":{"attempt":"a","native_ref":"n-0123","runtime_writer":"runtime/n-0123","runtime_seq":0,"outcome":"created"}}`},
		{"another kind does not take the runtime's entry", `{"v":1,"writer":"fixture","pid":0,"seq":1,"at_ms":1,"step":"","kind":"fixture.step","attrs":{"name":"s","runtime_seq":1}}`},
		{"a model.saw without its digest is an error", `{"v":1,"writer":"runtime/n","pid":0,"seq":1,"at_ms":1,"step":"","kind":"model.saw","attrs":{"endpoint":"e","message_id":"m","from":"a/b"}}`},
		{"an empty list of killed processes is an error: omit it", `{"v":1,"writer":"fixture","pid":0,"seq":1,"at_ms":1,"step":"","kind":"fixture.kill","attrs":{"pids":[]}}`},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if e, err := ledger.Decode([]byte(r.line)); err == nil {
				t.Fatalf("Decode accepted %q as %+v", r.line, e)
			}
		})
	}
	if _, err := ledger.Decode([]byte(base)); err != nil {
		t.Fatalf("the base line of the rows is itself rejected: %v", err)
	}
}

// TestEncodeRefusesInvalidEntries: nothing that Decode would refuse is written.
func TestEncodeRefusesInvalidEntries(t *testing.T) {
	ok := ledger.Entry{V: 1, Writer: "fixture", Seq: 1, AtMS: 1, Kind: ledger.KindFixtureStep, Attrs: ledger.Attrs{Name: "s"}}
	if _, err := ledger.Encode(ok); err != nil {
		t.Fatalf("the base entry is refused: %v", err)
	}
	mut := map[string]func(*ledger.Entry){
		"version":       func(e *ledger.Entry) { e.V = 0 },
		"writer":        func(e *ledger.Entry) { e.Writer = "nobody" },
		"sequence":      func(e *ledger.Entry) { e.Seq = 0 },
		"time":          func(e *ledger.Entry) { e.AtMS = -5 },
		"kind":          func(e *ledger.Entry) { e.Kind = "no.such.kind" },
		"missing attr":  func(e *ledger.Entry) { e.Attrs = ledger.Attrs{} },
		"foreign attr":  func(e *ledger.Entry) { e.Attrs.Lease = "00" },
		"negative pid":  func(e *ledger.Entry) { e.PID = -1 },
		"empty pidlist": func(e *ledger.Entry) { e.Kind = ledger.KindFixtureKill; e.Attrs = ledger.Attrs{PIDs: []int64{}} },
	}
	for name, f := range mut {
		t.Run(name, func(t *testing.T) {
			e := ok
			f(&e)
			if line, err := ledger.Encode(e); err == nil {
				t.Fatalf("Encode accepted %+v as %s", e, line)
			}
		})
	}
}

// TestKindTableCoversEveryKind: every kind constant is in the table, and the
// table has no kind without a constant, so a new kind cannot be written but
// unreadable.
func TestKindTableCoversEveryKind(t *testing.T) {
	want := []string{
		"adapter.started", "adapter.lock.acquired", "adapter.lock.refused",
		"session.create.received", "native.create.returned",
		"prompt.deliver.received", "prompt.forwarded", "interrupt.received", "close.received",
		"observed.sent", "knob.hit",
		"runtime.session.created", "runtime.prompt.received", "runtime.turn.started", "runtime.turn.ended",
		"runtime.interrupted", "runtime.closed", "runtime.killed",
		"model.saw", "client.call", "client.result",
		"fixture.step", "fixture.kill", "fixture.crash",
	}
	got := ledger.Kinds()
	if !reflect.DeepEqual(got, sortedCopy(want)) {
		t.Fatalf("Kinds() = %v, want %v", got, sortedCopy(want))
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
