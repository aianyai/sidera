package harness_test

import (
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/authz"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/record"
)

func TestAKnobHitIsWrittenToTheLedgerWithTheCallAndTheNode(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.MustDirect("b")
	h.Unreachable(true)
	if _, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Quiesce(); err != nil {
		t.Fatal(err)
	}
	log, err := h.Ledger()
	if err != nil {
		t.Fatal(err)
	}
	hits := log.Kind(ledger.KindKnobHit)
	if len(hits) == 0 {
		t.Fatal("no knob.hit entry")
	}
	e := hits[0]
	if e.Writer != "fixture" || e.Attrs.Knob != "unreachable" ||
		!strings.Contains(e.Attrs.Detail, "call=send") || !strings.Contains(e.Attrs.Detail, "node="+harness.NodeID().String()) {
		t.Errorf("entry %+v", e)
	}
}

func TestTheNodeHasRealKeysAndOnlyItsOwnReportsVerify(t *testing.T) {
	h := harness.New(t, harness.Options{})
	h.MustDirect("a")
	h.MustDirect("b")
	a, err := ident.ParseAddress(h.Addr("a"))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := h.Loopback().Send(h.Ctx(), harness.NodeID(), a.Endpoint, []byte("not an envelope"))
	if err == nil {
		t.Fatalf("garbage was taken in: %+v", rc)
	}
	if _, err := h.VerifyReport(record.Record{Type: envelope.StatusReportType}); err == nil {
		t.Error("an empty record verified")
	}
}

func TestOptionsReplaceTheRulesAndTheSettingsBeforeTheFirstStart(t *testing.T) {
	empty, err := authz.NewPolicy(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(t, harness.Options{
		Policy:       &config.PolicyFile{Version: 1, Policy: empty},
		NodeSettings: `{"node_queue_limit": 7}`,
	})
	if got := len(h.Config().Policy().Policy.Rules()); got != 0 {
		t.Errorf("%d rules", got)
	}
	if got := h.Config().Node().Settings.NodeQueueLimit; got != 7 {
		t.Errorf("node queue limit %d", got)
	}
	h.Restart()
	if got := h.Config().Node().Settings.NodeQueueLimit; got != 7 {
		t.Errorf("node queue limit %d after a restart: the patch is on the disk", got)
	}
}

func TestANodeOnTheRealFileSystemRunsAndCannotBeCrashed(t *testing.T) {
	h := harness.New(t, harness.Options{FS: harness.RealFS, DiskFree: func() uint64 { return 1 }})
	h.MustDirect("a")
	h.MustDirect("b")
	_, err := h.Agent("a").Send(h.Ctx(), fakeagent.SendInput{To: []string{h.Addr("b")}, Body: "x"})
	if fakeagent.CodeOf(err) != "storage_full" {
		t.Errorf("a send on a volume with no room: %v, want storage_full from the stand-in", err)
	}
	h.Restart()
	h.MustDirect("a")
}
