package delivery

import (
	"errors"
	"testing"
	"time"
)

func TestCheckDedup(t *testing.T) {
	key := DedupKey{SenderNode: node(1), Message: "m1", Recipient: endpoint(9)}
	d1, d2 := BodyDigest{1}, BodyDigest{2}
	stored := NewDedupRecord(key, d1, ReportBy(at(10*time.Minute)))
	rows := []struct {
		rule     string
		existing *DedupRecord
		digest   BodyDigest
		want     DedupVerdict
	}{
		{"an unknown key is new", nil, d1, DedupNew},
		{"the same key and body returns the original record", &stored, d1, DedupReplay},
		{"the same key with a different body is a conflict", &stored, d2, DedupConflict},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			if got := CheckDedup(r.existing, r.digest); got != r.want {
				t.Errorf("got %v, want %v", got, r.want)
			}
		})
	}
	t.Run("a key differing in any part finds nothing in a store indexed by key", func(t *testing.T) {
		store := map[DedupKey]DedupRecord{key: stored}
		lookup := func(k DedupKey) *DedupRecord {
			if rec, ok := store[k]; ok {
				return &rec
			}
			return nil
		}
		if got := CheckDedup(lookup(key), d1); got != DedupReplay {
			t.Fatalf("the stored key itself: %v, want replay", got)
		}
		other := map[string]DedupKey{
			"another sender node": {SenderNode: node(2), Message: "m1", Recipient: endpoint(9)},
			"another message id":  {SenderNode: node(1), Message: "m2", Recipient: endpoint(9)},
			"another recipient":   {SenderNode: node(1), Message: "m1", Recipient: endpoint(8)},
		}
		for what, k := range other {
			if got := CheckDedup(lookup(k), d1); got != DedupNew {
				t.Errorf("%s: %v, want new", what, got)
			}
		}
	})
	t.Run("the record is kept until report_by", func(t *testing.T) {
		if !stored.RetainUntil.Equal(ReportBy(at(10 * time.Minute))) {
			t.Errorf("retain until %v", stored.RetainUntil)
		}
	})
}

func TestDedupReply(t *testing.T) {
	key := DedupKey{SenderNode: node(1), Message: "c1", Recipient: endpoint(9)}
	rec := NewDedupRecord(key, BodyDigest{1}, ReportBy(at(10*time.Minute)))
	t.Run("a control reply is stored with the dedup record and returned on replay", func(t *testing.T) {
		reply := []byte("closed")
		stored, err := rec.WithReply(reply)
		if err != nil {
			t.Fatal(err)
		}
		reply[0] = 'X'
		if CheckDedup(&stored, BodyDigest{1}) != DedupReplay || string(stored.Reply) != "closed" || !stored.HasReply {
			t.Errorf("record %+v", stored)
		}
	})
	t.Run("an empty reply still counts as stored", func(t *testing.T) {
		stored, _ := rec.WithReply(nil)
		if !stored.HasReply {
			t.Error("HasReply not set")
		}
	})
	t.Run("a second reply is refused: the signal would have run twice", func(t *testing.T) {
		stored, _ := rec.WithReply([]byte("a"))
		if _, err := stored.WithReply([]byte("b")); !errors.Is(err, ErrReplyStored) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("storing a reply does not change the original record value", func(t *testing.T) {
		_, _ = rec.WithReply([]byte("a"))
		if rec.HasReply {
			t.Error("original mutated")
		}
	})
}
