package fakeagent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var start = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func now() time.Time { return start }

// scripted is an Agent that answers from a script and remembers what it was
// asked, and what the ledger held at the moment it was asked.
type scripted struct {
	mem    *ledger.Memory
	asked  []string
	during []int // client.call entries on the ledger when each call began
	send   func(fakeagent.SendInput) (fakeagent.SendResult, error)
	recv   func(fakeagent.RecvInput) (fakeagent.RecvResult, error)
	err    error
}

func (s *scripted) note(method string) {
	s.asked = append(s.asked, method)
	l, _ := s.mem.Log()
	s.during = append(s.during, len(l.Kind(ledger.KindClientCall)))
}

func (s *scripted) Send(_ context.Context, in fakeagent.SendInput) (fakeagent.SendResult, error) {
	s.note("send")
	if s.send != nil {
		return s.send(in)
	}
	return fakeagent.SendResult{MessageID: "m1", To: in.To}, s.err
}

func (s *scripted) Reply(context.Context, fakeagent.ReplyInput) (fakeagent.SendResult, error) {
	s.note("reply")
	return fakeagent.SendResult{MessageID: "m2"}, s.err
}

func (s *scripted) Recv(_ context.Context, in fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	s.note("recv")
	if s.recv != nil {
		return s.recv(in)
	}
	return fakeagent.RecvResult{}, s.err
}

func (s *scripted) Ack(context.Context, fakeagent.AckInput) (fakeagent.AckResult, error) {
	s.note("ack")
	return fakeagent.AckResult{}, s.err
}

func (s *scripted) Control(context.Context, fakeagent.ControlInput) (fakeagent.SendResult, error) {
	s.note("control")
	return fakeagent.SendResult{MessageID: "m3"}, s.err
}

func (s *scripted) Declare(context.Context, fakeagent.DeclareInput) error {
	s.note("declare")
	return s.err
}

func (s *scripted) Status(context.Context, fakeagent.StatusInput) (fakeagent.StatusResult, error) {
	s.note("status")
	return fakeagent.StatusResult{}, s.err
}

func recorded(t *testing.T, inner *scripted, opts fakeagent.RecordingOptions) (fakeagent.Agent, *ledger.Memory) {
	t.Helper()
	mem := ledger.NewMemory()
	inner.mem = mem
	w, err := mem.Open("agent/sess-1", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return fakeagent.Recording(inner, w, "sess-1", opts), mem
}

func entries(t *testing.T, mem *ledger.Memory) []ledger.Entry {
	t.Helper()
	l, err := mem.Log()
	if err != nil {
		t.Fatal(err)
	}
	return l.Entries()
}

func TestErrorCategories(t *testing.T) {
	cats := fakeagent.Categories()
	want := []fakeagent.Category{
		fakeagent.CategoryInvalidArgument, fakeagent.CategoryUnauthenticated, fakeagent.CategoryNotFound,
		fakeagent.CategoryConflict, fakeagent.CategoryRefused, fakeagent.CategoryBackpressure,
		fakeagent.CategoryUnavailable, fakeagent.CategoryInternal, fakeagent.CategoryOutcomeUnknown,
	}
	if fmt.Sprint(cats) != fmt.Sprint(want) {
		t.Fatalf("Categories() = %v, want the categories of the interface in their documented order %v", cats, want)
	}
	for _, c := range want {
		if !c.Valid() {
			t.Errorf("%q is not valid", c)
		}
	}
	if fakeagent.Category("mystery").Valid() || fakeagent.Category("").Valid() {
		t.Error("a category outside the vocabulary is valid")
	}
}

func TestErrorAccessors(t *testing.T) {
	err := fmt.Errorf("while sending: %w", &fakeagent.Error{Category: fakeagent.CategoryRefused, Code: "backlog_full", Message: "the recipient's backlog is full"})
	if got := fakeagent.CategoryOf(err); got != fakeagent.CategoryRefused {
		t.Errorf("CategoryOf = %q", got)
	}
	if got := fakeagent.CodeOf(err); got != "backlog_full" {
		t.Errorf("CodeOf = %q", got)
	}
	var fe *fakeagent.Error
	if !errors.As(err, &fe) || fe.Error() != "refused/backlog_full: the recipient's backlog is full" {
		t.Errorf("Error() = %q", fe.Error())
	}
	if got := fakeagent.CategoryOf(errors.New("plain")); got != fakeagent.CategoryInternal {
		t.Errorf("CategoryOf(a plain error) = %q, want internal", got)
	}
	if got := fakeagent.CodeOf(errors.New("plain")); got != "unclassified" {
		t.Errorf("CodeOf(a plain error) = %q", got)
	}
	if fakeagent.CategoryOf(nil) != "" || fakeagent.CodeOf(nil) != "" {
		t.Error("a nil error has a category")
	}
}

// TestRecordingWritesTheCallBeforeTheActionAndTheResultAfter: "the message was
// sent" is judged on the result entry only, never on the daemon's state, and the
// call entry is on the ledger before the action starts, so a kill in the middle
// still leaves the attempt on record.
func TestRecordingWritesTheCallBeforeTheActionAndTheResultAfter(t *testing.T) {
	inner := &scripted{}
	a, mem := recorded(t, inner, fakeagent.RecordingOptions{})
	ctx := context.Background()
	res, err := a.Send(ctx, fakeagent.SendInput{To: []string{"n/e"}, Body: "a secret body", IdemKey: "k1"})
	if err != nil || res.MessageID != "m1" {
		t.Fatalf("Send: %+v, %v", res, err)
	}
	if inner.during[0] != 1 {
		t.Fatalf("the ledger held %d client.call entries when the action began, want 1", inner.during[0])
	}
	got := entries(t, mem)
	if len(got) != 2 || got[0].Kind != ledger.KindClientCall || got[1].Kind != ledger.KindClientResult {
		t.Fatalf("entries %+v", got)
	}
	if c := got[0].Attrs; c.Action != "send" || c.IdemKey != "k1" || c.Endpoint != "sess-1" {
		t.Errorf("call %+v", c)
	}
	if r := got[1].Attrs; r.Action != "send" || r.Category != "ok" || r.MessageID != "m1" || r.IdemKey != "k1" {
		t.Errorf("result %+v", r)
	}
	for _, e := range got {
		line, _ := ledger.Encode(e)
		if bytes.Contains(line, []byte("secret")) {
			t.Errorf("the ledger holds a body: %s", line)
		}
	}
}

func TestRecordingRecordsEveryMethodAndItsFailure(t *testing.T) {
	failure := &fakeagent.Error{Category: fakeagent.CategoryBackpressure, Code: "node_queue_full", Message: "full"}
	cases := []struct {
		action string
		call   func(fakeagent.Agent) error
	}{
		{"send", func(a fakeagent.Agent) error {
			_, err := a.Send(context.Background(), fakeagent.SendInput{})
			return err
		}},
		{"reply", func(a fakeagent.Agent) error {
			_, err := a.Reply(context.Background(), fakeagent.ReplyInput{})
			return err
		}},
		{"recv", func(a fakeagent.Agent) error {
			_, err := a.Recv(context.Background(), fakeagent.RecvInput{})
			return err
		}},
		{"ack", func(a fakeagent.Agent) error { _, err := a.Ack(context.Background(), fakeagent.AckInput{}); return err }},
		{"control", func(a fakeagent.Agent) error {
			_, err := a.Control(context.Background(), fakeagent.ControlInput{})
			return err
		}},
		{"declare", func(a fakeagent.Agent) error { return a.Declare(context.Background(), fakeagent.DeclareInput{}) }},
		{"status", func(a fakeagent.Agent) error {
			_, err := a.Status(context.Background(), fakeagent.StatusInput{})
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			a, mem := recorded(t, &scripted{err: failure}, fakeagent.RecordingOptions{})
			if err := c.call(a); !errors.Is(err, failure) {
				t.Fatalf("the error was changed on the way: %v", err)
			}
			got := entries(t, mem)
			if len(got) != 2 {
				t.Fatalf("%d entries, want call and result", len(got))
			}
			if got[0].Attrs.Action != c.action || got[1].Attrs.Action != c.action {
				t.Errorf("actions %q, %q, want %q", got[0].Attrs.Action, got[1].Attrs.Action, c.action)
			}
			if r := got[1].Attrs; r.Category != "backpressure" || r.Code != "node_queue_full" {
				t.Errorf("result %+v", r)
			}
		})
	}
	t.Run("an error of no category is internal", func(t *testing.T) {
		a, mem := recorded(t, &scripted{err: errors.New("boom")}, fakeagent.RecordingOptions{})
		_, _ = a.Send(context.Background(), fakeagent.SendInput{})
		if r := entries(t, mem)[1].Attrs; r.Category != "internal" || r.Code != "unclassified" {
			t.Fatalf("result %+v", r)
		}
	})
}

func page(msgs ...adapterproto.PageMessage) fakeagent.RecvResult {
	p := adapterproto.RecvPage{Lease: "0123456789abcdef0123456789abcdef", Messages: msgs}
	raw, _ := json.Marshal(p)
	return fakeagent.RecvResult{Raw: raw, Page: p}
}

// TestRecordingCanRecordWhatTheModelSawOutsideATurn: an agent that is not a
// runtime's model records the messages of its pages itself, in exactly the
// entries the runtime writes for a page it hands its model.
func TestRecordingCanRecordWhatTheModelSawOutsideATurn(t *testing.T) {
	p := page(
		adapterproto.PageMessage{ID: "m1", From: "alice/agent", Body: "first"},
		adapterproto.PageMessage{ID: "m2", From: "alice/agent", PossiblyDuplicate: true, Body: "second", Part: &adapterproto.PagePart{Index: 1, Total: 2, Label: "part 1/2", Next: &adapterproto.PageNext{Msg: "m2", Part: 2}}},
	)
	inner := &scripted{recv: func(fakeagent.RecvInput) (fakeagent.RecvResult, error) { return p, nil }}

	withSaw, mem := recorded(t, inner, fakeagent.RecordingOptions{ModelSaw: true})
	if _, err := withSaw.Recv(context.Background(), fakeagent.RecvInput{Frontend: adapterproto.FrontendCLI}); err != nil {
		t.Fatal(err)
	}
	var saw []ledger.Attrs
	for _, e := range entries(t, mem) {
		if e.Kind == ledger.KindModelSaw {
			saw = append(saw, e.Attrs)
			if e.Writer != "agent/sess-1" {
				t.Errorf("model.saw written by %s", e.Writer)
			}
		}
	}
	want := fakeruntime.ModelSaw("sess-1", p.Page, nil)
	if fmt.Sprintf("%+v", deref(saw)) != fmt.Sprintf("%+v", deref(want)) {
		t.Fatalf("model.saw attributes\n got %+v\nwant %+v", deref(saw), deref(want))
	}

	without, mem2 := recorded(t, &scripted{recv: inner.recv}, fakeagent.RecordingOptions{})
	if _, err := without.Recv(context.Background(), fakeagent.RecvInput{}); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries(t, mem2) {
		if e.Kind == ledger.KindModelSaw {
			t.Fatal("a Recording without ModelSaw wrote model.saw: the runtime of the turn already writes it, and two writers would count a message twice")
		}
	}
}

// deref makes attributes comparable as text: they hold a pointer.
func deref(in []ledger.Attrs) []string {
	out := make([]string, len(in))
	for i, a := range in {
		dup := "-"
		if a.PossiblyDuplicate != nil {
			dup = fmt.Sprint(*a.PossiblyDuplicate)
		}
		a.PossiblyDuplicate = nil
		out[i] = fmt.Sprintf("%+v dup=%s", a, dup)
	}
	return out
}

func TestRecordingFeedbackClassifierMarksSystemNotices(t *testing.T) {
	p := page(adapterproto.PageMessage{ID: "m1", From: "node/system", Body: "d1 was rejected"})
	inner := &scripted{recv: func(fakeagent.RecvInput) (fakeagent.RecvResult, error) { return p, nil }}
	a, mem := recorded(t, inner, fakeagent.RecordingOptions{ModelSaw: true, Feedback: func(from, body string) (string, bool) { return "rejected", from == "node/system" }})
	_, _ = a.Recv(context.Background(), fakeagent.RecvInput{})
	for _, e := range entries(t, mem) {
		if e.Kind == ledger.KindModelSaw && e.Attrs.Feedback != "rejected" {
			t.Fatalf("model.saw %+v: the classifier's kind is missing", e.Attrs)
		}
	}
}

// TestRecordingKeepsTheRawPageUnchanged: the page is printed exactly as the
// backend returned it, because the daemon's check before an acknowledgement
// compares the text the model saw with what it handed out.
func TestRecordingKeepsTheRawPageUnchanged(t *testing.T) {
	p := page(adapterproto.PageMessage{ID: "m1", From: "a/b", Body: "x"})
	inner := &scripted{recv: func(fakeagent.RecvInput) (fakeagent.RecvResult, error) { return p, nil }}
	a, _ := recorded(t, inner, fakeagent.RecordingOptions{ModelSaw: true})
	got, err := a.Recv(context.Background(), fakeagent.RecvInput{})
	if err != nil || !bytes.Equal(got.Raw, p.Raw) {
		t.Fatalf("Recv: %q, %v", got.Raw, err)
	}
}

func TestStatusInputNamesTheMessage(t *testing.T) {
	in := fakeagent.StatusInput{MessageID: "m1"}
	if in.MessageID != "m1" {
		t.Fatal()
	}
}
