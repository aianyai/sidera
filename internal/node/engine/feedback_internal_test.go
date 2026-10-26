package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
)

func feedbackItem(t testing.TB, msg string, recipient byte, kind delivery.Kind, at time.Time) delivery.Item {
	t.Helper()
	return delivery.Item{
		Ref: delivery.DeliveryRef{
			Sender:  ident.Address{Node: nodeOf(t, 1), Endpoint: idOf(t, 50)},
			Message: delivery.MessageID(msg), Recipient: ident.Address{Node: nodeOf(t, 2), Endpoint: idOf(t, recipient)},
		},
		Feedback: delivery.Feedback{Kind: kind, State: delivery.StateRejected, Reject: delivery.RejectBacklogFull}, At: at,
	}
}

// A notice has one key for one set of items: a notice formed again after a crash is
// the same record, and any other set is another.
func TestTheKeyOfANoticeFollowsFromItsItemsAlone(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	a := feedbackItem(t, "m-1", 60, delivery.KindRejected, at)
	b := feedbackItem(t, "m-2", 61, delivery.KindRejected, at)
	sender := a.Ref.Sender
	node := nodeOf(t, 1)
	notice := func(items ...delivery.Item) delivery.Notice { return delivery.Notice{Sender: sender, Items: items} }

	key := noticeKey(node, notice(a, b))
	if key != noticeKey(node, notice(a, b)) {
		t.Error("the same notice has two keys")
	}
	if key != noticeKey(node, notice(b, a)) {
		t.Error("the order of the items changes the key")
	}
	later := a
	later.At = at.Add(time.Hour)
	if key != noticeKey(node, notice(later, b)) {
		t.Error("the time an item was decided changes the key: a notice formed again later would be a second one")
	}
	for name, other := range map[string]delivery.Notice{
		"another set of items": notice(a),
		"another message":      notice(feedbackItem(t, "m-3", 60, delivery.KindRejected, at), b),
		"another kind":         notice(feedbackItem(t, "m-1", 60, delivery.KindDeliveryUnknown, at), b),
		"another sender":       {Sender: ident.Address{Node: sender.Node, Endpoint: idOf(t, 51)}, Items: []delivery.Item{a, b}},
	} {
		if noticeKey(node, other) == key {
			t.Errorf("%s gives the same key", name)
		}
	}
	if err := envelope.ValidMessageID(key.Message); err != nil {
		t.Errorf("the identifier of a notice is not a message identifier: %v", err)
	}
	if !key.Sender.IsSystem() || key.SenderNode != node || key.Recipient != sender.Endpoint {
		t.Errorf("the key %+v is not the one of a notice of the system endpoint of the node to its sender", key)
	}
}

// The identifier of a notice names each kind by its name and not by its number, so a
// kind inserted among the others later does not change the identifier of a notice
// of a kind that was there, which a notice formed again after an upgrade would need.
// The oracle spells the identifier out of the notice of one item, independently of
// noticeKey.
func TestTheKeyOfANoticeNamesItsKindsByTheirNames(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	it := feedbackItem(t, "m-1", 60, delivery.KindRejected, at)
	it.Source = "out/source-record"
	line := "m-1 " + it.Ref.Recipient.String() + " rejected out/source-record"
	sum := sha256.Sum256([]byte(it.Ref.Sender.String() + "\n" + line))
	want := delivery.MessageID("n-" + hex.EncodeToString(sum[:16]))
	if got := noticeKey(nodeOf(t, 1), delivery.Notice{Sender: it.Ref.Sender, Items: []delivery.Item{it}}).Message; got != want {
		t.Errorf("the identifier is %s, want %s", got, want)
	}
}

// A notice that could not be written goes back to the queue as it was: its items
// are pending again and the endpoint has not been counted as told.
func TestUnsendPutsBackWhatFlushTookOut(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	p := delivery.DefaultFeedbackParams()
	var q delivery.FeedbackQueue
	for i, msg := range []string{"m-1", "m-2"} {
		if err := q.Add(feedbackItem(t, msg, byte(60+i), delivery.KindRejected, at)); err != nil {
			t.Fatal(err)
		}
	}
	before := delivery.FeedbackQueue{Pending: append([]delivery.Item(nil), q.Pending...)}
	now := at.Add(p.MergeWindow)
	notices, err := q.Flush(now, p)
	if err != nil || len(notices) != 2 || len(q.Pending) != 0 || len(q.Sent) != 2 {
		t.Fatalf("Flush gave %d notices (%v), left %d pending and %d sent", len(notices), err, len(q.Pending), len(q.Sent))
	}
	unsend(&q, notices[1:], now)
	if len(q.Sent) != 1 || len(q.Pending) != 1 || q.Pending[0].Ref.Message != "m-2" {
		t.Errorf("after putting one notice back: %d sent, pending %+v, want one sent and m-2 pending", len(q.Sent), q.Pending)
	}
	unsend(&q, notices[:1], now)
	if len(q.Sent) != 0 || len(q.Pending) != len(before.Pending) {
		t.Errorf("after putting both back: %d sent, %d pending, want none sent and %d pending", len(q.Sent), len(q.Pending), len(before.Pending))
	}
	again, err := q.Flush(now, p)
	if err != nil || len(again) != 2 {
		t.Errorf("the notices put back are not due again: %d, %v", len(again), err)
	}
}

// A source record the loop wrote enters the view in one place, putOut, which hands the
// queue of feedback the items the record holds that the view did not: a write that
// went round it could decide feedback that nobody tells. Only putOut and the build of
// the view from a scan call view.setOut.
func TestEverySourceRecordTheLoopWritesEntersTheViewThroughPutOut(t *testing.T) {
	allowed := map[string]bool{"putOut": true, "buildView": true}
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range files {
		for name, f := range pkg.Files {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || allowed[fn.Name.Name] {
					continue
				}
				ast.Inspect(fn, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "setOut" {
							t.Errorf("%s: %s calls setOut; a source record enters the view through putOut", name, fn.Name.Name)
						}
					}
					return true
				})
			}
		}
	}
}
