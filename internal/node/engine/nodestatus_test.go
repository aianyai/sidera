package engine_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/adapterreg"
	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/record"
)

// The status shows what the loop knows at one command, so its parts agree: who the
// node is and where it is in its life, the reminders, the clock and the pause, the
// certificate and how long it lasts, the room the node has, the deliveries it holds
// by state and its endpoints. The expectations are written out by hand from the
// decided numbers (a certificate lasts 30 days, the queue holds 10,000 unfinished
// deliveries, the reserve is 256 MiB), not read from the code that sets them.
func TestStatusShowsWhatTheNodeKnowsOfItself(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a", "b")
	send(t, h, "a", "b", "waits for b to read it")
	quiet(t, h)
	e := h.Engine()
	st := nodeStatusOf(t, e)

	if st.Node != harness.NodeID() || st.Phase != engine.PhaseRunning || !st.StartedAt.Equal(harness.DefaultStart) {
		t.Errorf("who and when: %s, %v, started %v", st.Node, st.Phase, st.StartedAt)
	}
	if want := harness.DefaultStart.Add(30 * 24 * time.Hour); !st.Certificate.NotAfter.Equal(want) || st.Certificate.Remaining != 30*24*time.Hour {
		t.Errorf("certificate until %v with %v left; want %v and 30 days", st.Certificate.NotAfter, st.Certificate.Remaining, want)
	}
	if len(st.Reminders) != 1 || st.Reminders[0] != engine.ReminderRevocationBackupUnconfirmed {
		t.Errorf("reminders %v, want the backup that is not confirmed", st.Reminders)
	}
	if st.Clock.RollbackPaused || !st.Clock.PausedSince.IsZero() || st.Clock.HighWater.Before(harness.DefaultStart) {
		t.Errorf("clock %+v", st.Clock)
	}
	if c := st.Capacity; c.NodeQueueLimit != 10000 || c.DiskReserveBytes != 256<<20 || c.OpenDeliveries != 1 || c.OutboundBacklog != 0 || c.DiskFreeBytes == 0 {
		t.Errorf("capacity %+v; want a queue of 10,000, a reserve of 256 MiB, one delivery that waits and none unanswered", c)
	}
	if len(st.Deliveries) != 1 || st.Deliveries[delivery.StateAccepted] != 1 {
		t.Errorf("deliveries %v, want one accepted", st.Deliveries)
	}
	if len(st.Endpoints) != 2 || st.Endpoints[0].Kind != adapterreg.KindDirect || st.Endpoints[1].Status != store.EndpointOpen {
		t.Errorf("endpoints %+v, want the two direct endpoints, open", st.Endpoints)
	}
	if st.AlertsOpen != 0 {
		t.Errorf("%d alerts open", st.AlertsOpen)
	}

	// Time passes and the clock is set back: the certificate has less left, the mark
	// is not lowered, and the pause is shown with the time it began.
	h.Advance(time.Hour)
	st = nodeStatusOf(t, e)
	if st.Certificate.Remaining != 30*24*time.Hour-time.Hour {
		t.Errorf("remaining after an hour: %v", st.Certificate.Remaining)
	}
	mark := h.Clock().Now()
	h.Set(mark.Add(-30 * time.Minute))
	st = nodeStatusOf(t, e)
	if !st.Clock.RollbackPaused || !st.Clock.PausedSince.Equal(mark.Add(-30*time.Minute)) || !st.Clock.HighWater.Equal(mark) {
		t.Errorf("clock after it was set back: %+v; want the pause since %v and the mark %v kept", st.Clock, mark.Add(-30*time.Minute), mark)
	}
}

// The export of the self-revocation is the statement on disk, armored, with the
// digest the owner confirms the backup with; it is made outside the loop and
// stored nowhere, because the statement can retire the node.
func TestTheExportOfTheSelfRevocationIsTheStatementOnDiskAndIsStoredNowhere(t *testing.T) {
	h := nodeWith(t, harness.Options{}, "a")
	onDisk, err := h.Disk().ReadFile("keys/self-revocation.rec")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := record.Decode(onDisk, record.DefaultMaxInput)
	if err != nil {
		t.Fatal(err)
	}
	storedBefore := len(scanOf(t, h).Admin)
	got, err := h.Engine().ExportRevocation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != rec.Digest() {
		t.Errorf("the export names the digest %s, want %s", got.Digest, rec.Digest())
	}
	lines := strings.Split(strings.TrimRight(string(got.Armor), "\n"), "\n")
	if len(lines) < 3 || lines[0] != "-----BEGIN SIDERA SELF-REVOCATION-----" || lines[len(lines)-1] != "-----END SIDERA SELF-REVOCATION-----" {
		t.Fatalf("the export is not armored: %q", got.Armor)
	}
	body, err := base64.StdEncoding.DecodeString(strings.Join(lines[1:len(lines)-1], ""))
	if err != nil || string(body) != string(onDisk) {
		t.Errorf("the armor holds %d bytes (%v), want the %d of the statement on disk", len(body), err, len(onDisk))
	}
	for _, l := range lines[1 : len(lines)-1] {
		if len(l) > 64 {
			t.Errorf("an armor line of %d characters, want at most 64", len(l))
		}
	}
	quiet(t, h)
	if sc := scanOf(t, h); len(sc.Admin) != storedBefore {
		t.Errorf("%d results stored under state/admin after an export, %d before", len(sc.Admin), storedBefore)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Engine().ExportRevocation(gone); err == nil {
		t.Error("an export was made for a caller that had gone")
	}
}

func nodeStatusOf(t testing.TB, e *engine.Engine) engine.NodeStatus {
	t.Helper()
	st, err := e.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return st
}
