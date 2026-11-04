package state

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

// A transition is on disk when Update returns, a refused one leaves nothing, and a
// reopened store holds exactly what the first one did.
func TestUpdateIsWrittenBeforeItReturnsAndSurvivesReopening(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(d *Data) error {
		c := &Card{ID: "t_1", Endpoint: "e", Mode: "message", ClaimedAt: t0}
		c.BeginAttempt(1, t0)
		d.Cards[c.ID] = c
		d.MarkProcessed("m1", t0)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	refused := errors.New("refused")
	if err := s.Update(func(d *Data) error { d.Cards["t_1"].Sent("m2", t0); return refused }); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if got := s.View().Cards["t_1"]; got.Phase != PhaseAttempting || got.MessageID != "" {
		t.Errorf("a refused update changed the data: %+v", got)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d := reopened.View()
	if c := d.Cards["t_1"]; c == nil || c.Attempt != 1 || !c.AttemptAt.Equal(t0) || c.Phase != PhaseAttempting {
		t.Errorf("reopened card: %+v", c)
	}
	if !d.Seen("m1", t0.Add(adapterproto.ReceivedRedeliveryWindow-time.Minute)) || d.Seen("m1", t0.Add(adapterproto.ReceivedRedeliveryWindow)) || d.Seen("other", t0) {
		t.Error("the table of processed messages is wrong after reopening")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, fileName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("state file mode: %v %v", fi, err)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestOpenRefusesAFileItCannotRead(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":       "not json",
		"other version": `{"version":9,"cards":{},"endpoints":{},"sessions":{},"processed":{}}`,
		"unknown field": `{"version":1,"cards":{},"endpoints":{},"sessions":{},"processed":{},"extra":1}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
}

func TestMarkProcessedDropsExpiredRecords(t *testing.T) {
	d := newData()
	d.MarkProcessed("old", t0)
	d.MarkProcessed("new", t0.Add(adapterproto.ReceivedRedeliveryWindow))
	if _, ok := d.Processed["old"]; ok {
		t.Error("expired record kept")
	}
}

func TestPruneKeepsTheParentOfAnOpenSession(t *testing.T) {
	d := newData()
	old := t0.Add(-30 * 24 * time.Hour)
	d.Cards["gone"] = &Card{ID: "gone", Phase: PhaseFinished, FinishedAt: old}
	d.Cards["recent"] = &Card{ID: "recent", Phase: PhaseFinished, FinishedAt: t0.Add(-time.Hour)}
	d.Cards["live"] = &Card{ID: "live", Phase: PhaseSent, ClaimedAt: old}
	d.Cards["parent"] = &Card{ID: "parent", Phase: PhaseFinished, FinishedAt: old, Peer: "sess", Session: true}
	d.Cards["closed"] = &Card{ID: "closed", Phase: PhaseFinished, FinishedAt: old, Peer: "sess-2", Session: true}
	d.Sessions["sess"] = &Session{Address: "sess"}
	d.Prune(t0, 7*24*time.Hour)
	if len(d.Cards) != 3 || d.Cards["gone"] != nil || d.Cards["closed"] != nil {
		t.Errorf("kept: %v", d.Cards)
	}
}
