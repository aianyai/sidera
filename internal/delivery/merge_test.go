package delivery

import (
	"testing"
	"time"
)

var testParams = FeedbackParams{MergeWindow: time.Minute, RateCap: 2, RateWindow: 10 * time.Minute}

func item(sender, target, msg string, kind Kind, when time.Duration) Item {
	senderEp := map[string]byte{"s1": 1, "s2": 2}[sender]
	tn := map[string]byte{"n1": 11, "n2": 12}[target[:2]]
	te := byte(target[2] - '0')
	fb := Feedback{Kind: kind}
	if kind == KindUnreachable {
		fb.Cause = CauseNetwork
	}
	return Item{
		Ref:      DeliveryRef{Sender: addr(100, senderEp), Message: MessageID(msg), Recipient: addr(tn, te)},
		Feedback: fb,
		At:       t0.Add(when),
	}
}

func TestMergeKey(t *testing.T) {
	a := item("s1", "n11", "m1", KindUnreachable, 0)
	rows := []struct {
		rule      string
		x, y      Item
		sameGroup bool
	}{
		{"unreachable hints for different messages to one node merge", a, item("s1", "n12", "m2", KindUnreachable, 0), true},
		{"unreachable hints to different nodes stay apart", a, item("s1", "n21", "m2", KindUnreachable, 0), false},
		{"unreachable hints of different sender endpoints stay apart", a, item("s2", "n11", "m1", KindUnreachable, 0), false},
		{"expiry notices for different messages to one node merge", item("s1", "n11", "m1", KindExpiredUnconfirmed, 0), item("s1", "n12", "m2", KindExpiredUnconfirmed, 0), true},
		{"expiry notices to different nodes stay apart", item("s1", "n11", "m1", KindExpiredUnconfirmed, 0), item("s1", "n21", "m2", KindExpiredUnconfirmed, 0), false},
		{"expiry notices of different sender endpoints stay apart", item("s1", "n11", "m1", KindExpiredUnconfirmed, 0), item("s2", "n11", "m1", KindExpiredUnconfirmed, 0), false},
		{"an expiry notice is a notice of its own, apart from the unreachable hint of the same node", item("s1", "n11", "m1", KindUnreachable, 0), item("s1", "n11", "m1", KindExpiredUnconfirmed, 0), false},
		{"the correction of an expiry follows its message, not the node", item("s1", "n11", "m1", KindExpiredUnconfirmed, 0), item("s1", "n11", "m1", KindExpiredCorrected, 0), false},
		{"blocked reports for different messages to one target endpoint merge", item("s1", "n11", "m1", KindDeliveryBlocked, 0), item("s1", "n11", "m2", KindDeliveryBlocked, 0), true},
		{"blocked reports for different target endpoints stay apart", item("s1", "n11", "m1", KindDeliveryBlocked, 0), item("s1", "n12", "m1", KindDeliveryBlocked, 0), false},
		{"a recovery is a notice of its own, apart from the blocks", item("s1", "n11", "m1", KindDeliveryBlocked, 0), item("s1", "n11", "m1", KindBlockedRecovered, 0), false},
		{"recoveries of one target endpoint merge", item("s1", "n11", "m1", KindBlockedRecovered, 0), item("s1", "n11", "m2", KindBlockedRecovered, 0), true},
		{"anomalies of the two recipients of one message merge", item("s1", "n11", "m1", KindRejected, 0), item("s1", "n12", "m1", KindDeliveryUnknown, 0), true},
		{"anomalies of different messages stay apart", item("s1", "n11", "m1", KindRejected, 0), item("s1", "n11", "m2", KindRejected, 0), false},
		{"a correction merges with the anomalies of its message", item("s1", "n11", "m1", KindDeliveryUnknown, 0), item("s1", "n11", "m1", KindUnknownCorrected, 0), true},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := MergeKeyOf(r.x) == MergeKeyOf(r.y); got != r.sameGroup {
				t.Errorf("same key = %v, want %v", got, r.sameGroup)
			}
		})
	}
}

func TestFeedbackParamsValidate(t *testing.T) {
	rows := []struct {
		rule string
		p    FeedbackParams
		ok   bool
	}{
		{"complete parameters are valid", testParams, true},
		{"the zero value is refused: the parameters are undecided", FeedbackParams{}, false},
		{"a missing merge window is refused", FeedbackParams{RateCap: 1, RateWindow: time.Minute}, false},
		{"a missing rate cap is refused", FeedbackParams{MergeWindow: time.Minute, RateWindow: time.Minute}, false},
		{"a missing rate window is refused", FeedbackParams{MergeWindow: time.Minute, RateCap: 1}, false},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if err := r.p.Validate(); (err == nil) != r.ok {
				t.Errorf("err = %v", err)
			}
		})
	}
	t.Run("flush refuses unset parameters", func(t *testing.T) {
		var q FeedbackQueue
		if _, err := q.Flush(t0, FeedbackParams{}); err == nil {
			t.Error("want an error")
		}
	})
}

func TestFeedbackQueue(t *testing.T) {
	t.Run("twenty messages to an offline node give one merged notice", func(t *testing.T) {
		var q FeedbackQueue
		for i := 0; i < 20; i++ {
			it := item("s1", "n11", "m"+string(rune('a'+i)), KindUnreachable, time.Duration(i)*time.Second)
			if err := q.Add(it); err != nil {
				t.Fatal(err)
			}
		}
		got, err := q.Flush(at(30*time.Second), testParams)
		if err != nil || len(got) != 0 {
			t.Fatalf("flushed %d notices before the merge window ended (err %v)", len(got), err)
		}
		got, err = q.Flush(at(time.Minute), testParams)
		if err != nil || len(got) != 1 || len(got[0].Items) != 20 {
			t.Fatalf("notices %d, err %v", len(got), err)
		}
		if len(q.Pending) != 0 {
			t.Errorf("%d items left pending", len(q.Pending))
		}
	})
	t.Run("twenty messages to a node that stayed offline give one merged expiry notice", func(t *testing.T) {
		var q FeedbackQueue
		for i := 0; i < 20; i++ {
			it := item("s1", "n11", "m"+string(rune('a'+i)), KindExpiredUnconfirmed, time.Duration(i)*time.Second)
			if err := q.Add(it); err != nil {
				t.Fatal(err)
			}
		}
		got, err := q.Flush(at(time.Minute), testParams)
		if err != nil || len(got) != 1 || len(got[0].Items) != 20 {
			t.Fatalf("notices %+v, err %v", got, err)
		}
	})
	t.Run("a multi-recipient message gives one notice for all its affected recipients", func(t *testing.T) {
		var q FeedbackQueue
		for _, tgt := range []string{"n11", "n12", "n21"} {
			_ = q.Add(item("s1", tgt, "m1", KindRejected, 0))
		}
		got, _ := q.Flush(at(time.Minute), testParams)
		if len(got) != 1 || len(got[0].Items) != 3 {
			t.Errorf("notices %+v", got)
		}
	})
	t.Run("a blocked endpoint gives one notice for all its blocked messages and one for the recovery", func(t *testing.T) {
		var q FeedbackQueue
		for _, m := range []string{"m1", "m2", "m3"} {
			_ = q.Add(item("s1", "n11", m, KindDeliveryBlocked, 0))
		}
		got, _ := q.Flush(at(time.Minute), testParams)
		if len(got) != 1 || len(got[0].Items) != 3 {
			t.Fatalf("blocked notices %+v", got)
		}
		for _, m := range []string{"m1", "m2", "m3"} {
			_ = q.Add(item("s1", "n11", m, KindBlockedRecovered, 10*time.Minute))
		}
		got, _ = q.Flush(at(12*time.Minute), testParams)
		if len(got) != 1 || len(got[0].Items) != 3 || got[0].Items[0].Feedback.Kind != KindBlockedRecovered {
			t.Errorf("recovery notices %+v", got)
		}
	})
	t.Run("the same feedback queued twice is sent once", func(t *testing.T) {
		var q FeedbackQueue
		it := item("s1", "n11", "m1", KindRejected, 0)
		_ = q.Add(it)
		_ = q.Add(it)
		got, _ := q.Flush(at(time.Minute), testParams)
		if len(got) != 1 || len(got[0].Items) != 1 {
			t.Errorf("notices %+v", got)
		}
	})
	t.Run("invalid items are refused", func(t *testing.T) {
		var q FeedbackQueue
		bad := item("s1", "n11", "m1", KindRejected, 0)
		bad.Feedback.Kind = 0
		if q.Add(bad) == nil {
			t.Error("kind 0 accepted")
		}
		bad = item("s1", "n11", "", KindRejected, 0)
		if q.Add(bad) == nil {
			t.Error("empty message id accepted")
		}
	})
	t.Run("a group counts its window from its oldest item", func(t *testing.T) {
		var q FeedbackQueue
		_ = q.Add(item("s1", "n11", "m1", KindUnreachable, 30*time.Second))
		_ = q.Add(item("s1", "n11", "m2", KindUnreachable, 0))
		if got, _ := q.Flush(at(59*time.Second), testParams); len(got) != 0 {
			t.Errorf("sent early: %+v", got)
		}
		if got, _ := q.Flush(at(time.Minute), testParams); len(got) != 1 || len(got[0].Items) != 2 {
			t.Errorf("notices %+v", got)
		}
	})
	t.Run("items arriving after a flush start a new notice", func(t *testing.T) {
		var q FeedbackQueue
		_ = q.Add(item("s1", "n11", "m1", KindUnreachable, 0))
		first, _ := q.Flush(at(time.Minute), testParams)
		_ = q.Add(item("s1", "n11", "m2", KindUnreachable, 2*time.Minute))
		second, _ := q.Flush(at(4*time.Minute), testParams)
		if len(first) != 1 || len(second) != 1 {
			t.Errorf("first %d second %d", len(first), len(second))
		}
	})
	t.Run("the per-endpoint cap holds notices back and sends them later, in order", func(t *testing.T) {
		var q FeedbackQueue
		for i, m := range []string{"m1", "m2", "m3"} {
			_ = q.Add(item("s1", "n11", m, KindRejected, time.Duration(i)*time.Second))
		}
		got, _ := q.Flush(at(2*time.Minute), testParams)
		if len(got) != 2 || got[0].Items[0].Ref.Message != "m1" || got[1].Items[0].Ref.Message != "m2" || len(q.Pending) != 1 {
			t.Fatalf("first flush %+v, pending %d", got, len(q.Pending))
		}
		if got, _ = q.Flush(at(5*time.Minute), testParams); len(got) != 0 {
			t.Fatalf("flush within the rate window sent %+v", got)
		}
		got, _ = q.Flush(at(13*time.Minute), testParams)
		if len(got) != 1 || got[0].Items[0].Ref.Message != "m3" || len(q.Pending) != 0 {
			t.Errorf("later flush %+v", got)
		}
	})
	t.Run("the cap counts per sender endpoint", func(t *testing.T) {
		var q FeedbackQueue
		for _, m := range []string{"m1", "m2", "m3"} {
			_ = q.Add(item("s1", "n11", m, KindRejected, 0))
			_ = q.Add(item("s2", "n11", m, KindRejected, 0))
		}
		got, _ := q.Flush(at(time.Minute), testParams)
		if len(got) != 4 {
			t.Errorf("notices %d, want 2 per sender", len(got))
		}
	})
	t.Run("flushing twice at the same instant sends nothing twice", func(t *testing.T) {
		var q FeedbackQueue
		_ = q.Add(item("s1", "n11", "m1", KindRejected, 0))
		a, _ := q.Flush(at(time.Minute), testParams)
		b, _ := q.Flush(at(time.Minute), testParams)
		if len(a) != 1 || len(b) != 0 {
			t.Errorf("a %d b %d", len(a), len(b))
		}
	})
}
