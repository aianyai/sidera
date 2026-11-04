package kanban

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The samples are real `--json` outputs of Hermes 0.22.2 with the owner's paths
// and names replaced. A Hermes release that renames or drops a field this client
// reads makes these fail.
func TestDecodeRecordedSamples(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		var cards []Card
		mustDecode(t, "list-ready.json", &cards)
		if len(cards) != 2 || cards[0].ID != "t_4fa05d4f" || cards[0].Status != StatusReady ||
			cards[0].Assignee != "sidera" || cards[0].Title != "-dash title" ||
			cards[0].Body != "second" || cards[0].CreatedBy != "sidera" || cards[0].SessionID != "" ||
			cards[0].CreatedAt.Unix() != 1793797286 || !cards[0].StartedAt.IsZero() {
			t.Fatalf("cards = %+v", cards)
		}
		if cards[1].Body != "" {
			t.Fatalf("a null body is empty, got %q", cards[1].Body)
		}
	})
	t.Run("show with runs", func(t *testing.T) {
		var d Detail
		mustDecode(t, "show-runs.json", &d)
		if d.Card.Status != StatusDone || d.Card.Assignee != "sidera:node-b" || d.LatestSummary != "s" ||
			d.Card.Result != "r" || !strings.Contains(d.Card.Body, "hello — ünï\\ncode") {
			t.Fatalf("detail = %+v", d)
		}
		if len(d.Runs) != 2 || d.Runs[0].Outcome != "blocked" || d.Runs[0].Summary != "delivery failed" ||
			d.Runs[1].Outcome != "completed" || d.Runs[1].Summary != "s" ||
			d.Runs[1].EndedAt.Before(d.Runs[1].StartedAt) {
			t.Fatalf("runs = %+v", d.Runs)
		}
		if len(d.Comments) == 0 || d.Comments[0].Author != "sidera" {
			t.Fatalf("comments = %+v", d.Comments)
		}
	})
	t.Run("show with a parent", func(t *testing.T) {
		var d Detail
		mustDecode(t, "show-parent.json", &d)
		if len(d.Parents) != 1 || d.Parents[0] != "t_fa6b0cea" || d.Card.Status != StatusReady {
			t.Fatalf("detail = %+v", d)
		}
	})
	t.Run("attachments", func(t *testing.T) {
		var atts []Attachment
		mustDecode(t, "attachments.json", &atts)
		if len(atts) != 2 || atts[0].Filename != "msg_01.txt" || atts[1].Filename != "msg_01 (1).txt" ||
			atts[0].Size != 22 || !strings.HasSuffix(atts[0].StoredPath, "/attachments/t_fa6b0cea/msg_01.txt") {
			t.Fatalf("attachments = %+v", atts)
		}
	})
	t.Run("notify-list", func(t *testing.T) {
		var subs []NotifySub
		mustDecode(t, "notify-list.json", &subs)
		if len(subs) != 2 {
			t.Fatalf("subs = %+v", subs)
		}
		want := NotifySub{TaskID: "t_fa6b0cea", Platform: "telegram", ChatID: "oc_chat1", ThreadID: "th1",
			UserID: "ou_u1", UserIDAlt: "on_u1", ChatType: "group", NotifierProfile: "work",
			DeliveryMode: DeliveryNotifyWake, CreatedAt: time.Unix(1793797344, 0), LastEventID: 10}
		got := subs[0]
		meta := got.DeliveryMetadata
		got.DeliveryMetadata = nil
		if !reflect.DeepEqual(got, want) || meta["guild_id"] != "g1" || meta["parent_chat_id"] != "pc1" {
			t.Fatalf("sub = %+v meta %v", got, meta)
		}
		if subs[1].UserID != "" || subs[1].DeliveryMode != DeliveryWake || subs[1].ThreadID != "" {
			t.Fatalf("sub = %+v", subs[1])
		}
	})
	t.Run("assignees", func(t *testing.T) {
		var as []Assignee
		mustDecode(t, "assignees.json", &as)
		if len(as) != 5 || as[0] != (Assignee{"default", true}) || as[3] != (Assignee{"sidera", false}) {
			t.Fatalf("assignees = %+v", as)
		}
	})
}

func mustDecode(t *testing.T, name string, v any) {
	t.Helper()
	if err := json.Unmarshal(sample(t, name), v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// A field the adapter relies on that is gone fails loudly; one it does not read
// may come and go.
func TestDecodeShapeChanges(t *testing.T) {
	card := `{"id":"t_1","title":"x","body":null,"assignee":"sidera","status":"ready","created_by":"u","session_id":"s1","created_at":1,"workspace":"new field"}`
	var c Card
	if err := json.Unmarshal([]byte(card), &c); err != nil || c.SessionID != "s1" {
		t.Fatalf("unknown field: card %+v err %v", c, err)
	}
	for _, drop := range []string{"id", "title", "body", "assignee", "status", "created_by", "session_id", "created_at"} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(card), &m); err != nil {
			t.Fatal(err)
		}
		delete(m, drop)
		b, _ := json.Marshal(m)
		err := json.Unmarshal(b, &c)
		if err == nil || !strings.Contains(err.Error(), drop) {
			t.Errorf("dropping %q: err = %v", drop, err)
		}
	}
	// A column of the subscription that is gone: a copy would lose the routing.
	var sub NotifySub
	if err := json.Unmarshal([]byte(`{"task_id":"t","platform":"p","chat_id":"c","thread_id":"","user_id":null,"user_id_alt":null,"chat_type":"dm","notifier_profile":"x","delivery_mode":"notify"}`), &sub); err == nil ||
		!strings.Contains(err.Error(), "delivery_metadata") {
		t.Errorf("subscription without delivery_metadata: %v", err)
	}
}
