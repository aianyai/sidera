package fakeruntime_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
)

// goldenEvents are lines of the session record, written out by hand. The record
// is what the fake adapter reads to reconcile and to observe, as a real adapter
// reads the file its native runtime keeps, so its shape is a contract between
// the fake runtime and the fake adapter, which may be separate processes.
var goldenEvents = []struct {
	name  string
	event fakeruntime.Event
	line  string
}{
	{"the session is created", fakeruntime.Event{Seq: 1, AtMS: 1792832400000, Kind: fakeruntime.EventSession, Endpoint: "sess-1", Attempt: "00000000-0000-4000-8000-000000000001"},
		`{"seq":1,"at_ms":1792832400000,"event":"session","endpoint":"sess-1","attempt":"00000000-0000-4000-8000-000000000001"}`},
	{"a prompt arrives", fakeruntime.Event{Seq: 2, AtMS: 1792832400000, Kind: fakeruntime.EventPrompt, Attempt: "00000000-0000-4000-8000-000000000002", TextDigest: "ab"},
		`{"seq":2,"at_ms":1792832400000,"event":"prompt","attempt":"00000000-0000-4000-8000-000000000002","text_digest":"ab"}`},
	{"a turn starts for a prompt", fakeruntime.Event{Seq: 3, AtMS: 1792832401000, Kind: fakeruntime.EventTurnStarted, Turn: "t1", Attempt: "00000000-0000-4000-8000-000000000002"},
		`{"seq":3,"at_ms":1792832401000,"event":"turn_started","turn":"t1","attempt":"00000000-0000-4000-8000-000000000002"}`},
	{"a turn starts by itself", fakeruntime.Event{Seq: 4, AtMS: 1792832401000, Kind: fakeruntime.EventTurnStarted, Turn: "t2"},
		`{"seq":4,"at_ms":1792832401000,"event":"turn_started","turn":"t2"}`},
	{"a tool call", fakeruntime.Event{Seq: 5, AtMS: 1792832402000, Kind: fakeruntime.EventToolCall, Turn: "t1", Action: "recv"},
		`{"seq":5,"at_ms":1792832402000,"event":"tool_call","turn":"t1","action":"recv"}`},
	{"a tool result with the text the model saw", fakeruntime.Event{Seq: 6, AtMS: 1792832402000, Kind: fakeruntime.EventToolResult, Turn: "t1", Action: "recv", Lease: "0123456789abcdef0123456789abcdef", Frontend: "cli", Text: "{\"lease\":\"x\"}"},
		`{"seq":6,"at_ms":1792832402000,"event":"tool_result","turn":"t1","action":"recv","lease":"0123456789abcdef0123456789abcdef","frontend":"cli","text":"{\"lease\":\"x\"}"}`},
	{"a tool result that failed", fakeruntime.Event{Seq: 7, AtMS: 1792832402000, Kind: fakeruntime.EventToolResult, Turn: "t1", Action: "ack", Error: "refused"},
		`{"seq":7,"at_ms":1792832402000,"event":"tool_result","turn":"t1","action":"ack","error":"refused"}`},
	{"a turn ends", fakeruntime.Event{Seq: 8, AtMS: 1792832460000, Kind: fakeruntime.EventTurnEnded, Turn: "t1", Final: "completed"},
		`{"seq":8,"at_ms":1792832460000,"event":"turn_ended","turn":"t1","final":"completed"}`},
	{"an interruption", fakeruntime.Event{Seq: 9, AtMS: 1792832460000, Kind: fakeruntime.EventInterrupted},
		`{"seq":9,"at_ms":1792832460000,"event":"interrupted"}`},
	{"the session is closed", fakeruntime.Event{Seq: 10, AtMS: 1792832460000, Kind: fakeruntime.EventClosed},
		`{"seq":10,"at_ms":1792832460000,"event":"closed"}`},
	{"the native process is killed", fakeruntime.Event{Seq: 11, AtMS: 1792832460000, Kind: fakeruntime.EventKilled},
		`{"seq":11,"at_ms":1792832460000,"event":"killed"}`},
}

func TestGoldenRecordLines(t *testing.T) {
	for _, g := range goldenEvents {
		t.Run(g.name, func(t *testing.T) {
			got, err := fakeruntime.EncodeEvent(g.event)
			if err != nil {
				t.Fatalf("EncodeEvent: %v", err)
			}
			if string(got) != g.line {
				t.Fatalf("EncodeEvent:\n got %s\nwant %s", got, g.line)
			}
			back, err := fakeruntime.DecodeEvent([]byte(g.line))
			if err != nil {
				t.Fatalf("DecodeEvent: %v", err)
			}
			if !reflect.DeepEqual(back, g.event) {
				t.Fatalf("DecodeEvent:\n got %+v\nwant %+v", back, g.event)
			}
		})
	}
}

const baseEvent = `{"seq":1,"at_ms":1,"event":"session","endpoint":"e","attempt":"a"}`

func TestDecodeEventRejects(t *testing.T) {
	rows := []struct{ rule, line string }{
		{"an unknown member is an error", strings.Replace(baseEvent, `"seq":1`, `"seq":1,"extra":1`, 1)},
		{"a missing member is an error", strings.Replace(baseEvent, `"seq":1,`, ``, 1)},
		{"an unknown event is an error", strings.Replace(baseEvent, `"session"`, `"dream"`, 1)},
		{"a member the event does not take is an error", strings.Replace(baseEvent, `"attempt":"a"`, `"attempt":"a","turn":"t"`, 1)},
		{"a member the event requires is an error when missing", strings.Replace(baseEvent, `,"attempt":"a"`, ``, 1)},
		{"the sequence starts at 1", strings.Replace(baseEvent, `"seq":1`, `"seq":0`, 1)},
		{"a time is not negative", strings.Replace(baseEvent, `"at_ms":1`, `"at_ms":-1`, 1)},
		{"null is not a value", strings.Replace(baseEvent, `"attempt":"a"`, `"attempt":null`, 1)},
		{"members out of order are not the canonical line", `{"at_ms":1,"seq":1,"event":"session","endpoint":"e","attempt":"a"}`},
		{"a repeated member is an error", strings.Replace(baseEvent, `"seq":1`, `"seq":1,"seq":1`, 1)},
		{"an empty value is an error: omit it", strings.Replace(baseEvent, `"endpoint":"e"`, `"endpoint":""`, 1)},
		{"a turn end needs a final", `{"seq":1,"at_ms":1,"event":"turn_ended","turn":"t"}`},
		{"a final outside the vocabulary is an error", `{"seq":1,"at_ms":1,"event":"turn_ended","turn":"t","final":"vanished"}`},
		{"a trailing newline is not part of the line", baseEvent + "\n"},
		{"an empty line is an error", ``},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if e, err := fakeruntime.DecodeEvent([]byte(r.line)); err == nil {
				t.Fatalf("accepted %q as %+v", r.line, e)
			}
		})
	}
	if _, err := fakeruntime.DecodeEvent([]byte(baseEvent)); err != nil {
		t.Fatalf("the base line is rejected: %v", err)
	}
}

// FuzzDecodeEvent: decoding never panics and an accepted line is the canonical
// line of its event.
func FuzzDecodeEvent(f *testing.F) {
	for _, g := range goldenEvents {
		f.Add([]byte(g.line))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := fakeruntime.DecodeEvent(data)
		if err != nil {
			return
		}
		again, err := fakeruntime.EncodeEvent(e)
		if err != nil {
			t.Fatalf("an accepted event cannot be encoded: %v", err)
		}
		if !bytes.Equal(again, data) {
			t.Fatalf("decode then encode changed the line:\n in  %q\n out %q", data, again)
		}
	})
}

// FuzzReadRecord: reading a whole record never panics.
func FuzzReadRecord(f *testing.F) {
	var all []byte
	for _, g := range goldenEvents {
		all = append(all, g.line...)
		all = append(all, '\n')
	}
	f.Add(all)
	f.Add([]byte("garbage\n"))
	f.Add([]byte(goldenEvents[0].line))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = fakeruntime.ParseRecord(data)
	})
}

func TestParseRecordRejectsADamagedRecord(t *testing.T) {
	line := func(i int) string { return goldenEvents[i].line + "\n" }
	rows := []struct {
		rule string
		data string
	}{
		{"a gap in the sequence", line(0) + `{"seq":3,"at_ms":1,"event":"closed"}` + "\n"},
		{"a record that does not start at 1", `{"seq":2,"at_ms":1,"event":"closed"}` + "\n"},
		{"garbage between lines", line(0) + "nonsense\n" + line(1)},
		{"a last line without its newline", line(0) + strings.TrimSuffix(line(1), "\n")},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if _, err := fakeruntime.ParseRecord([]byte(r.data)); err == nil {
				t.Fatal("accepted a damaged record")
			}
		})
	}
	good := line(0) + line(1) + line(2)
	if evs, err := fakeruntime.ParseRecord([]byte(good)); err != nil || len(evs) != 3 {
		t.Fatalf("a good record: %v, %d events", err, len(evs))
	}
}
