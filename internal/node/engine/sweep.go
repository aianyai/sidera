package engine

import (
	"time"

	"github.com/aianyai/sidera/internal/node/store"
)

// The archive sweep takes what has served its retention out of the live files: the
// store plans it from a scan (store.PlanSweep) and the engine runs the plan, because
// the engine is the one writer and holds the view that must follow every removal.
//
// A pass is a periodic task that scans and plans in the loop, as the settlement of
// damage scans there, and then runs as commands of the loop that queue each other: the
// archive files past their retention first, then the summaries, in slices of
// Params.ChunkRemoves, then the removals, in chunks of at most that many files. Between
// two of them the loop takes what the inbox holds.
//
// So a pass stalls the loop once, for the full scan (the storage reference gives
// the measured times: about 3.2 s for 50,000 files and 11 to 13 s for 200,000, warm),
// and then for no longer than one bounded step at a time. While it stalls, a caller
// of the loop may be told loop_busy, which it retries. The scan is accepted in the
// loop for now because the view is the only reader of the files and the engine the
// only writer; a view that kept the scan's inputs up to date would take it out.
//
// Nothing is scanned again between the chunks: a chunk changes the files it names
// and nothing else. The plan is therefore as old as the pass, and a record the plan
// names is removed only while nothing has brought it back: before each chunk, the
// endpoints the plan has yet to remove are looked at (planIsStale), and a pass that
// finds one open again ends, so that the next one plans from a new scan.

const archiveSweepTask = "archive-sweep"

// AddArchiveSweep registers the archive sweep to run every period, the first time one
// period after Start. It is for the composition root, as AddPeriodic is, and the sweep
// is the engine's: the root chooses how often, not what is swept.
func (e *Engine) AddArchiveSweep(every time.Duration) error {
	return e.AddPeriodic(archiveSweepTask, every, e.beginSweep)
}

// sweepPass is the pass of the sweep that is running.
type sweepPass struct {
	at time.Time
	// expired are the archive files past their retention not removed yet.
	expired []string
	// summaries are the archive summaries not appended yet.
	summaries []store.ArchiveSummary
	// chunk holds what is not removed yet, in the order the store removes it: the
	// groups are cut from the front.
	chunk store.SweepChunk
	// records are the records of the scan the plan was made from, by file, so that
	// what a chunk removed is taken out of the view by the record it was.
	records map[store.FileRef]store.Record
}

// beginSweep is the periodic task: it plans a pass and queues the first command of it.
// A pass that is still running is left to finish; the next period asks again. The
// retention is judged by the wall clock, so nothing is swept while the clock is in
// doubt, as nothing is ticked.
func (e *Engine) beginSweep(in TaskInput) error {
	switch {
	case e.sweep != nil:
		return nil
	case e.Phase() != PhaseRunning:
		return nil
	case in.RollbackPaused:
		e.log.Info("the archive sweep waits: the wall clock is in doubt")
		return nil
	}
	sc, err := e.d.Store.Scan()
	if err != nil {
		return err
	}
	plan := store.PlanSweep(sc, in.Now)
	chunk := plan.Chunk()
	expired := store.ExpiredArchiveFiles(sc.Archive, in.Now, e.d.Config.Node().Settings.ArchiveRetention())
	if len(expired)+len(plan.Summaries)+len(chunk.Ctl)+len(chunk.Records)+len(chunk.Outs)+len(chunk.OutLosts)+len(chunk.Endpoints) == 0 {
		return nil
	}
	e.sweep = &sweepPass{at: in.Now, expired: expired, summaries: plan.Summaries, chunk: chunk, records: recordsByFile(sc)}
	e.later(cmdSweep{})
	return nil
}

// recordsByFile indexes the records of a scan by the file each is in.
func recordsByFile(sc *store.Scan) map[store.FileRef]store.Record {
	m := map[store.FileRef]store.Record{}
	add := func(dir string, rec store.Record) { m[store.FileRef{Dir: dir, Name: rec.Name()}] = rec }
	for _, r := range sc.In {
		add(store.InDir, r)
	}
	for _, r := range sc.Ctl {
		add(store.CtlDir, r)
	}
	for _, r := range sc.Endpoints {
		add(store.EndpointsDir, r)
	}
	for _, r := range sc.Checkpoints {
		add(store.CheckpointsDir, r)
	}
	for _, r := range sc.Admin {
		add(store.AdminDir, r)
	}
	for _, r := range sc.Alerts {
		add(store.AlertsDir, r)
	}
	return m
}

// cmdSweep is the next step of the pass that is running.
type cmdSweep struct{}

func (cmdSweep) gate() gate { return gateAlways }
func (cmdSweep) fail(error) {}
func (cmdSweep) run(e *Engine, now time.Time) {
	if e.sweep == nil {
		return
	}
	if e.Phase() >= PhaseStopping || e.fatal != nil || !e.sweepStep(now) {
		e.sweep = nil
		return
	}
	e.later(cmdSweep{})
}

// sweepStep does one bounded step of the pass and says whether there is more to do.
// A removal that failed ends the pass: whatever is still due is planned again by the
// next one, from a new scan.
func (e *Engine) sweepStep(now time.Time) bool {
	s := e.sweep
	if len(s.expired) > 0 {
		e.expireArchive(s)
		return true
	}
	if len(s.summaries) > 0 {
		e.appendSummaries(s)
		return true
	}
	if e.planIsStale(s) {
		e.log.Info("the archive sweep stops: an endpoint it was to remove is open again; the next one plans from a new scan")
		return false
	}
	head, rest := cutSweep(s.chunk, e.p.ChunkRemoves)
	s.chunk = rest
	err := e.retryingDamaged(now, func() error { return e.d.Store.ArchiveSweep(head) })
	if err != nil {
		e.classify(err)
		if e.fatal == nil {
			e.log.Warn("the archive sweep failed; what is due is planned again by the next one", "error", err.Error())
		}
		return false
	}
	e.forgetSwept(s, head)
	return !sweepEmpty(rest)
}

// expireArchive removes the next slice of the archive files past their retention.
// Like the summaries it is best effort and never holds the removals back: a failure
// is logged and the rest of this pass's files are given up, to be named again by the
// next pass's scan.
func (e *Engine) expireArchive(s *sweepPass) {
	n := min(len(s.expired), e.p.ChunkRemoves)
	err := e.d.Store.ArchiveExpire(s.expired[:n])
	if err == nil {
		s.expired = s.expired[n:]
		return
	}
	e.classify(err)
	if e.fatal == nil {
		e.log.Warn("the archive files past their retention could not be removed; the next sweep tries again", "files", len(s.expired), "error", err.Error())
	}
	s.expired = nil
}

// appendSummaries appends the next slice of the summaries. They are best effort: a
// failure is logged and the rest of this pass's summaries are given up, because the
// removals must not wait for a record of what they removed, least of all on a full
// disk, where the removals are what frees the space. The store has no log, so the
// failure is told here.
func (e *Engine) appendSummaries(s *sweepPass) {
	n := min(len(s.summaries), e.p.ChunkRemoves)
	err := e.d.Store.ArchiveSummaries(s.at, s.summaries[:n])
	if err == nil {
		s.summaries = s.summaries[n:]
		return
	}
	e.classify(err)
	if e.fatal == nil {
		e.log.Warn("the archive summaries could not be appended; the records are swept without them", "summaries", len(s.summaries), "error", err.Error())
	}
	s.summaries = nil
}

// cutSweep cuts the first max files (source records included, one each) off the
// chunk, in the order the store removes them, which is also the order the chunks
// must run in: a control record goes before the delivery derived from it, and the
// record of a closed endpoint after the files that hang on it.
func cutSweep(c store.SweepChunk, max int) (head, rest store.SweepChunk) {
	left := max
	head.Ctl, rest.Ctl, left = cut(c.Ctl, left)
	head.Records, rest.Records, left = cut(c.Records, left)
	head.Outs, rest.Outs, left = cut(c.Outs, left)
	head.OutLosts, rest.OutLosts, left = cut(c.OutLosts, left)
	head.Endpoints, rest.Endpoints, _ = cut(c.Endpoints, left)
	head.KeepTarget, rest.KeepTarget = c.KeepTarget, c.KeepTarget
	return head, rest
}

func cut[T any](s []T, budget int) (head, rest []T, left int) {
	n := min(len(s), budget)
	return s[:n:n], s[n:], budget - n
}

func sweepEmpty(c store.SweepChunk) bool {
	return len(c.Ctl)+len(c.Records)+len(c.Outs)+len(c.OutLosts)+len(c.Endpoints) == 0
}

// planIsStale says whether an endpoint the plan has yet to remove has been opened
// again since the plan was made. A direct or runtime endpoint keeps its identifier
// for ever, so one that was closed at the scan may be registered or admitted again
// before its chunk runs. Its record must stay, and so must the files that hang on
// it (attempts, checkpoints), which the plan names in chunks of their own before it
// reaches the record: leaving out the record alone is not enough, so what is left of
// the plan is dropped and made again from a new scan. A session endpoint's
// identifier is new every time, so it is never the one.
func (e *Engine) planIsStale(s *sweepPass) bool {
	for _, f := range s.chunk.Endpoints {
		ep, ok := s.records[f].(store.EndpointRecord)
		if !ok {
			continue
		}
		if st := e.view.endpoints[ep.ID]; st != nil && st.rec.Status != store.EndpointClosed {
			return true
		}
	}
	return false
}

// forgetSwept takes what a chunk removed out of the view, and out of the schedule
// with it: each record leaves by the record it was, and the entry of the schedule
// that stood for it is cleared by the view as it clears the entry of any record it
// drops. A source record leaves the outbox with the memory of its outbound work. An
// out-lost record is held by the queue of feedback and leaves it.
func (e *Engine) forgetSwept(s *sweepPass, c store.SweepChunk) {
	for _, group := range [][]store.FileRef{c.Ctl, c.Records, c.Endpoints} {
		for _, f := range group {
			if rec, ok := s.records[f]; ok {
				e.view.dropSwept(rec)
			}
		}
	}
	for _, o := range c.Outs {
		e.view.dropOut(o.Name())
		e.forgetOutbound(o.Name())
	}
	for _, o := range c.OutLosts {
		delete(e.fb.outLost, o.Name())
	}
	if n := len(c.Ctl) + len(c.Records) + len(c.Outs) + len(c.OutLosts) + len(c.Endpoints); n > 0 {
		e.log.Info("the archive sweep removed records", "files", n)
	}
}
