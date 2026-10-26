package engine_test

import (
	"context"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/store"
	"github.com/aianyai/sidera/internal/node/testkit/harness"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The archive sweep as the node runs it: a periodic task that takes what has served
// its retention out of the live files and out of the engine's view.

const sweepEvery = time.Hour

// sweepNode is a node of two endpoints, a and b, that runs the sweep every hour.
func sweepNode(t *testing.T, o harness.Options) *harness.H {
	t.Helper()
	o.ArchiveSweepEvery = sweepEvery
	return nodeWith(t, o, "a", "b")
}

// rejectedAndTold is a message that nobody reads, from a to b, to the point where
// both nodes' records are final but the notice that tells a is still to be read: b's
// queue time ran out, a learned it and was told once.
func rejectedAndTold(t *testing.T, h *harness.H) adapterproto.MessageID {
	t.Helper()
	msg := send(t, h, "a", "b", "nobody reads it")
	quiet(t, h)
	h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	h.Advance(mergeWindow)
	quiet(t, h)
	if got := toldKinds(t, h, "a"); len(got) != 1 {
		t.Fatalf("a was told %v, want the rejection once", got)
	}
	return msg
}

// passes runs n periods of the sweep, each followed by the node going quiet.
func passes(t *testing.T, h *harness.H, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		h.Advance(sweepEvery)
		quiet(t, h)
	}
}

func recordCount(sc *store.Scan) int {
	return len(sc.In) + len(sc.Out) + len(sc.OutLost) + len(sc.Ctl) + len(sc.Alerts)
}

func viewDiff(t *testing.T, h *harness.H) []string {
	t.Helper()
	diff, err := engine.ViewDiffersFromDisk(context.Background(), h.Engine(), scanOf(t, h))
	if err != nil {
		t.Fatalf("ViewDiffersFromDisk: %v", err)
	}
	return diff
}

// archived are the summaries of every archive file of the disk.
func archived(t *testing.T, h *harness.H) []store.ArchiveSummary {
	t.Helper()
	var out []store.ArchiveSummary
	for _, name := range scanOf(t, h).Archive {
		data, err := h.Disk().ReadFile(path.Join(store.ArchiveDir, name))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, store.ParseArchive(data).Summaries...)
	}
	return out
}

// The periodic task runs the sweep: what has served its retention is removed, one
// summary of each record that left goes to the archive, and the view of the engine
// is what a scan of the disk builds.
func TestThePeriodicSweepRemovesWhatHasServedItsRetention(t *testing.T) {
	h := sweepNode(t, harness.Options{})
	msg := rejectedAndTold(t, h)
	if recordCount(scanOf(t, h)) == 0 {
		t.Fatal("the node holds no record to sweep")
	}
	// The notice that told a is itself a delivery to a, which a does not read: its
	// queue time runs out, and then its retention.
	h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
	quiet(t, h)
	passes(t, h, 3)
	h.Advance(3 * 24 * time.Hour)
	quiet(t, h)
	passes(t, h, 3)

	sc := scanOf(t, h)
	if recordCount(sc) != 0 {
		t.Fatalf("records are left after the sweeps: in %d out %d out-lost %d", len(sc.In), len(sc.Out), len(sc.OutLost))
	}
	if diff := viewDiff(t, h); len(diff) != 0 {
		t.Errorf("the view differs from the disk after the sweep: %v", diff)
	}
	var of []string // the kind of each summary that is about msg
	for _, s := range archived(t, h) {
		if strings.Contains(s.Key, string(msg)) {
			of = append(of, string(s.Kind))
		}
	}
	slices.Sort(of)
	if !slices.Equal(of, []string{"in", "out"}) {
		t.Errorf("the archive holds summaries of kinds %v for %s, want its target and its source record", of, msg)
	}
	// A restart finds nothing to tell again.
	h.Restart()
	quiet(t, h)
	if got := toldKinds(t, h, "a"); len(got) != 0 {
		t.Errorf("a was told %v after the sweep and a restart, want nothing", got)
	}
}

// A summary that cannot be appended does not stop the sweep: the records go, the
// failure is in the log, and the node goes on. The sweep is what frees space on a full
// disk, so it never waits for the record of what it removed.
func TestASummaryThatCannotBeAppendedDoesNotStopTheSweep(t *testing.T) {
	for _, c := range []struct {
		what string
		err  error
	}{
		{"an I/O error", syscall.EIO},
		{"a full disk", fsxtest.DiskFull},
	} {
		t.Run(c.what, func(t *testing.T) {
			h := sweepNode(t, harness.Options{})
			rejectedAndTold(t, h)
			rule := h.Faults().Fail(fsxtest.Match{Tx: "archive-summaries"}, c.err)
			h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
			quiet(t, h)
			passes(t, h, 3)
			h.Advance(3 * 24 * time.Hour)
			quiet(t, h)
			passes(t, h, 3)
			if rule.Hits() == 0 {
				t.Fatal("no summary was appended: the fault did not meet the sweep")
			}
			if sc := scanOf(t, h); recordCount(sc) != 0 {
				t.Errorf("the sweep did not remove the records while the summaries failed: in %d out %d", len(sc.In), len(sc.Out))
			}
			if got := archived(t, h); len(got) != 0 {
				t.Errorf("the archive holds %d summaries that the fault should have refused", len(got))
			}
			if !slices.ContainsFunc(h.Log().Messages(), func(m string) bool { return strings.Contains(m, "archive summaries could not be appended") }) {
				t.Errorf("the failure is not in the log: %v", h.Log().Messages())
			}
			if err := h.Engine().Err(); err != nil {
				t.Errorf("the engine failed: %v", err)
			}
		})
	}
}

// A notice is the proof that its items were told. While a record still holds an item it
// covers (the clearing of it failed), the notice is kept however old, and the start
// that follows takes the item off the record by it: the endpoint is told once.
func TestANoticeIsNotSweptWhileTheItemItCoversIsStillOnItsRecord(t *testing.T) {
	h := sweepNode(t, harness.Options{})
	clearing := h.Faults().Fail(fsxtest.Match{Tx: "feedback-clear"}, syscall.EIO)
	rejectedAndTold(t, h)
	if clearing.Hits() == 0 {
		t.Fatal("the clearing of the item was not attempted")
	}
	first := noticesFor(t, h, "a")
	if len(first) != 1 {
		t.Fatalf("a holds %d notices, want 1", len(first))
	}
	// The notice ends and passes its retention, and the sweep runs: the item is still
	// on the source record, so neither is removed.
	h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
	quiet(t, h)
	h.Advance(3 * 24 * time.Hour)
	quiet(t, h)
	passes(t, h, 3)
	got := noticesFor(t, h, "a")
	if len(got) != 1 {
		t.Fatalf("a holds %d notices after the sweeps while the item is not cleared, want the one that covers it", len(got))
	}
	if !got[0].in.Record.State.IsTerminal() {
		t.Fatalf("the notice is %s, not final: the scenario does not reach the sweep", got[0].in.Record.State)
	}
	if pending(t, h, "a") == 0 {
		t.Fatal("the item was cleared: the scenario does not hold it")
	}

	h.Restart()
	quiet(t, h)
	passes(t, h, 3)
	if got := pending(t, h, "a"); got != 0 {
		t.Errorf("%d items are pending after the start: the notice should have cleared them", got)
	}
	// The notice that covered the item is still the one the endpoint was told by: a
	// notice written again would be a record of the later time.
	for _, n := range noticesFor(t, h, "a") {
		if !n.in.AcceptedAt.Equal(first[0].in.AcceptedAt) {
			t.Errorf("a was told again, at %v, after the notice of %v", n.in.AcceptedAt, first[0].in.AcceptedAt)
		}
	}
}

// A sweep does not hold the loop for more than a chunk: no transaction of it removes more
// than Params.ChunkRemoves records, and a larger plan takes several.
func TestTheSweepRemovesInChunksOfAtMostChunkRemoves(t *testing.T) {
	const chunk = 2
	var mu sync.Mutex
	removed, sweeps, most := 0, 0, 0
	obs := observerFunc(func(p fsx.Point, _ txn.Kind) {
		if p.Tag.Tx != "archive-sweep" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case p.Step == fsx.StepRemove && p.Tag.Role == "record":
			removed++
		case p.Step == fsx.StepCommit:
			sweeps++
			most = max(most, removed)
			removed = 0
		}
	})
	h := sweepNode(t, harness.Options{Observer: obs, Params: engine.Params{ChunkRemoves: chunk}})
	for i := 0; i < 4; i++ {
		send(t, h, "a", "b", "nobody reads it")
	}
	quiet(t, h)
	h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
	quiet(t, h)
	h.Advance(pullInterval)
	quiet(t, h)
	h.Advance(mergeWindow)
	quiet(t, h)
	h.Advance(delivery.DefaultEndpointMaxQueue + 2*time.Minute)
	quiet(t, h)
	h.Advance(3 * 24 * time.Hour)
	quiet(t, h)
	passes(t, h, 3)
	if sc := scanOf(t, h); recordCount(sc) != 0 {
		t.Fatalf("records are left: in %d out %d", len(sc.In), len(sc.Out))
	}
	mu.Lock()
	defer mu.Unlock()
	if most > chunk || most == 0 {
		t.Errorf("a transaction of the sweep removed %d records at most, want between 1 and %d", most, chunk)
	}
	if sweeps < 4 {
		t.Errorf("%d transactions of the sweep, want several: the plan is larger than a chunk", sweeps)
	}
}

type observerFunc func(fsx.Point, txn.Kind)

func (f observerFunc) Reached(p fsx.Point, k txn.Kind) { f(p, k) }

// dueAdminResult is an administrative result whose retention is over, written on the
// disk of a node that is down: the sweep has a record to remove from the first period.
func dueAdminResult(t testing.TB, h *harness.H, b byte) store.AdminRecord {
	t.Helper()
	rec := store.AdminRecord{
		Key: requestKey(b), Fingerprint: fingerprintOf("due"), Op: "endpoint.close",
		Result: []byte(`{"closed":true}`), At: harness.DefaultStart.Add(-48 * time.Hour), RetainUntil: harness.DefaultStart.Add(-time.Hour),
	}
	data, err := store.EncodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	writeOnDisk(t, h, store.AdminDir, rec.Name(), data)
	return rec
}

func hasFile(sc *store.Scan, dir, name string) bool {
	switch dir {
	case store.AdminDir:
		return slices.ContainsFunc(sc.Admin, func(r store.AdminRecord) bool { return r.Name() == name })
	case store.AttemptsDir:
		return slices.ContainsFunc(sc.Attempts, func(r store.AttemptsRecord) bool { return r.Name() == name })
	case store.EndpointsDir:
		return slices.ContainsFunc(sc.Endpoints, func(r store.EndpointRecord) bool { return r.Name() == name })
	}
	panic("hasFile: " + dir)
}

// The plan is made from a scan, and runs in chunks that the loop interleaves with its
// inbox. An endpoint that was closed at the scan and is registered again between two
// chunks keeps its record and every file that hangs on it that the plan has not
// removed yet: the pass ends, and the next one plans from a new scan. Leaving out the
// record alone would remove its attempts, which are in a chunk of their own.
func TestAPassEndsWhenAClosedEndpointItWasToRemoveIsRegisteredAgainBetweenChunks(t *testing.T) {
	var once sync.Once
	reached, release := make(chan struct{}), make(chan struct{})
	// The loop is held at the first chunk's commit, so that the registration is in the
	// inbox when the chunk ends and runs before the next one.
	obs := observerFunc(func(p fsx.Point, _ txn.Kind) {
		if p.Tag.Tx == "archive-sweep" && p.Step == fsx.StepCommit {
			once.Do(func() { close(reached); <-release })
		}
	})
	h := sweepNode(t, harness.Options{Observer: obs, Params: engine.Params{ChunkRemoves: 1}})
	h.MustDirect("c")
	c := endpointOf(t, h, "c")
	if _, err := h.Engine().CloseEndpoint(h.Ctx(), engine.CloseRequest{Endpoint: c}); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	h.Stop()
	due := dueAdminResult(t, h, 1)
	attempts := store.AttemptsRecord{Endpoint: c}
	data, err := store.EncodeRecord(attempts)
	if err != nil {
		t.Fatal(err)
	}
	writeOnDisk(t, h, store.AttemptsDir, attempts.Name(), data)
	h.Start()
	quiet(t, h)
	closed := store.EndpointRecord{ID: c}.Name()

	// The plan: the result, the attempts, then the endpoint, one chunk each.
	h.Advance(sweepEvery)
	<-reached
	registered := make(chan error, 1)
	go func() { _, err := h.Direct("c"); registered <- err }()
	for deadline := time.Now().Add(10 * time.Second); engine.InboxLen(h.Engine()) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("the registration did not reach the inbox of the loop")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-registered; err != nil {
		t.Fatalf("registering the endpoint again: %v", err)
	}
	quiet(t, h)

	sc := scanOf(t, h)
	if hasFile(sc, store.AdminDir, due.Name()) {
		t.Error("the first chunk did not run: the result is still there")
	}
	if !hasFile(sc, store.AttemptsDir, attempts.Name()) {
		t.Error("the attempts of the endpoint that is open again were removed")
	}
	if !hasFile(sc, store.EndpointsDir, closed) {
		t.Fatal("the record of the endpoint that is open again was removed")
	}
	if !slices.ContainsFunc(h.Log().Messages(), func(m string) bool { return strings.Contains(m, "is open again") }) {
		t.Errorf("the pass did not say why it stopped: %v", h.Log().Messages())
	}
	// The next pass plans from a new scan, in which the endpoint is open.
	passes(t, h, 2)
	sc = scanOf(t, h)
	if !hasFile(sc, store.AttemptsDir, attempts.Name()) || !hasFile(sc, store.EndpointsDir, closed) {
		t.Error("a later pass removed the files of the endpoint that is open")
	}
	if diff := viewDiff(t, h); len(diff) != 0 {
		t.Errorf("the view differs from the disk: %v", diff)
	}
}

// Nothing is swept while the wall clock is in doubt, because the retention is judged
// by it: several periods pass and what is due stays. Once the owner has cleared the
// pause, the next period removes it.
func TestNoSweepWhileTheClockIsInDoubt(t *testing.T) {
	h := sweepNode(t, harness.Options{RollbackPaused: true})
	h.Stop()
	due := dueAdminResult(t, h, 1)
	h.Start()
	quiet(t, h)
	passes(t, h, 3)
	if !hasFile(scanOf(t, h), store.AdminDir, due.Name()) {
		t.Fatal("a result that is due was removed while the clock is in doubt")
	}
	if !slices.ContainsFunc(h.Log().Messages(), func(m string) bool { return strings.Contains(m, "wall clock is in doubt") }) {
		t.Errorf("the sweep did not say that it waits: %v", h.Log().Messages())
	}
	if _, err := h.Engine().Execute(h.Ctx(), clearCommand(73, "clear")); err != nil {
		t.Fatal(err)
	}
	quiet(t, h)
	passes(t, h, 1)
	if hasFile(scanOf(t, h), store.AdminDir, due.Name()) {
		t.Error("the result that is due is still there one period after the pause was cleared")
	}
}

// The archive files past the retention the owner set are removed by the sweep, and
// those within it stay: the retention is the node's setting, 30 days unless the owner
// changed it.
func TestTheSweepRemovesTheArchiveFilesPastTheRetention(t *testing.T) {
	h := sweepNode(t, harness.Options{})
	h.Stop()
	today := harness.DefaultStart.UTC()
	retention := config.DefaultArchiveRetentionDays
	older := []time.Time{today.AddDate(0, 0, -retention-1), today.AddDate(0, 0, -retention-40)}
	younger := []time.Time{today.AddDate(0, 0, -retention), today.AddDate(0, 0, -1), today.AddDate(0, 0, 1)}
	for _, d := range append(slices.Clone(older), younger...) {
		writeOnDisk(t, h, store.ArchiveDir, store.ArchiveDay(d), []byte("\n"))
	}
	h.Start()
	quiet(t, h)
	passes(t, h, 1)
	var kept []string
	for _, name := range scanOf(t, h).Archive {
		kept = append(kept, name)
	}
	var want []string
	for _, d := range younger {
		want = append(want, store.ArchiveDay(d))
	}
	slices.Sort(kept)
	slices.Sort(want)
	if !slices.Equal(kept, want) {
		t.Errorf("archive files after the sweep: %v, want only %v", kept, want)
	}
}

// A source record whose delivery waits for the pull before its close is not final,
// and the sweep leaves it however many periods pass. The pull is made once report_by
// has passed; here its call waits on the clock for longer than the window the target
// keeps its records for, so the delivery stays accepted for the whole window, and
// closes at the window's end without the pull. The record is swept after that, and
// at no period before its retention.
func TestASourceRecordWaitingForItsFinalPullIsNotSweptBeforeItClosesNorBeforeItsRetention(t *testing.T) {
	h := sweepNode(t, harness.Options{})
	msg := send(t, h, "a", "b", "its target cannot be asked")
	quiet(t, h)
	h.Unreachable(true)
	reportBy := sourceDelivery(t, h, msg).ReportBy
	until := engine.FinalPullUntil(reportBy)
	retention, _ := store.RetainUntil(outOf(t, h, msg))
	h.Advance(reportBy.Sub(h.Clock().Now()) - 30*time.Minute)
	quiet(t, h)
	if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
		t.Fatalf("the source record is %s before report_by, want accepted", d.State)
	}
	h.Delay(2 * (until.Sub(reportBy)))
	h.Advance(time.Hour)
	quiet(t, h)

	present := func() bool {
		for _, o := range scanOf(t, h).Out {
			if o.Key.Message == delivery.MessageID(msg) {
				return true
			}
		}
		return false
	}
	for h.Clock().Now().Before(until.Add(-time.Hour)) {
		passes(t, h, 1)
		if d := sourceDelivery(t, h, msg); d.State != delivery.StateAccepted {
			t.Fatalf("at %v the source record is %s while its pull is under way, want accepted", h.Clock().Now(), d.State)
		}
	}
	// The window ends, the tick closes the delivery without the pull, and the record
	// goes at the first period at or after its retention, never before.
	var last delivery.State
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("the source record is never swept")
		}
		passes(t, h, 1)
		if !present() {
			break
		}
		last = sourceDelivery(t, h, msg).State
	}
	if last != delivery.StateClosedUnconfirmed {
		t.Errorf("the last state of the source record before it was swept is %s, want closed_unconfirmed", last)
	}
	if now := h.Clock().Now(); now.Before(retention) {
		t.Errorf("the source record was swept at %v, before its retention %v", now, retention)
	}
}
