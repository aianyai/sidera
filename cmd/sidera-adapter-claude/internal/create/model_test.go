package create

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"
)

// This file holds an independent model of the world the adapter lives in: a
// session file, a process table, the runtime's session registry and a clock
// that ticks in seconds. The model knows how a Claude turn process behaves
// (what it writes and when, how it can die, hang or start slowly, that
// --session-id fails on an existing session and --resume on a missing one) and
// how outsiders behave (a process of the user that carries the session uuid,
// or an interactive resume that only the registry shows); it knows nothing
// about how Decide reasons. Every expectation below is computed from the
// model's own truth, with its own comparison of PIDs and start seconds. The
// driver plays the adapter: observe, decide, act, commit, and be killed between
// any two of those steps; after a failure it keeps observing and deciding.
//
// One assumption is not verified against the real runtime: --resume on a
// session file that exists but is empty or lacks the first prompt works and
// writes the prompt. If it did not, the attempt would end in StartsExhausted,
// a visible failure.

// simProc is a process in the model's table. Session processes are Claude turn
// processes of the session under test, started by the adapter; outsiders work
// on the session without being the adapter's; the others are unrelated
// processes that took over a PID the system reused.
type simProc struct {
	pid, start int // start is a tick
	session    bool
	outsider   string // "" for none, "argv" (carries the uuid) or "registry" (shown only by the registry)
	kind       string
	writeAt    int // offset of the first write
	events     int // writes done: 0 create file, 1 write uuid, 2 write first prompt
	dieAfter   int // die when about to do this write; -1 never
	hangAfter  int // stop progressing when about to do this write; -1 never
	exitGap    int // ticks after the last write before a normal exit
	regAt      int // offset at which the registry entry is written; -1 never
}

// carriesUUID: the uuid is on the process's command line.
func (p *simProc) carriesUUID() bool { return p.session || p.outsider == "argv" }

type behaviour struct {
	pDeath, pHang, pSlow, pPIDReuse float64
	pRegLate, pRegNever             float64 // registry entry of a turn process written some ticks late, or never
	pOutside                        float64 // an outsider appears right after the first turn process started
	pOutsideRegistry                float64 // ... of which only the registry shows it
}

type world struct {
	rng  *rand.Rand
	beh  behaviour
	tick int

	fileExists, fileUUID, filePrompt bool
	sessionsCreated                  int

	procs    map[int]*simProc
	registry map[int]int // pid -> start tick, of the processes that registered
	nextPID  int

	spawns          int // successful spawns
	violations      []string
	pidReuseCreated int

	outsiderAt   int // tick at which the outsider appears; 0 none scheduled
	outsiderKind string
	outsiders    int // outsiders that appeared

	regLate, regNever int // turn processes whose registry entry is late or missing
}

func unixOfTick(tick int) int64 { return t0.Unix() + int64(tick) }

func newWorld(seed uint64, b behaviour) *world {
	return &world{
		rng: rand.New(rand.NewPCG(seed, 99)), beh: b,
		procs: map[int]*simProc{}, registry: map[int]int{}, nextPID: 1000,
	}
}

func (w *world) violate(format string, args ...any) {
	w.violations = append(w.violations, fmt.Sprintf("tick %d: ", w.tick)+fmt.Sprintf(format, args...))
}

func (w *world) time() time.Time { return t0.Add(time.Duration(w.tick) * time.Second) }

func (w *world) sorted(keep func(*simProc) bool) []*simProc {
	var out []*simProc
	for _, p := range w.procs {
		if keep(p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

// ours are the adapter's turn processes.
func (w *world) ours() []*simProc { return w.sorted(func(p *simProc) bool { return p.session }) }

// carrying are the processes with the uuid on their command line.
func (w *world) carrying() []*simProc { return w.sorted((*simProc).carriesUUID) }

// registryOnly are the outsiders that only the registry shows.
func (w *world) registryOnly() []*simProc {
	return w.sorted(func(p *simProc) bool { return p.outsider == "registry" })
}

// writers are all processes that work on the session.
func (w *world) writers() []*simProc {
	return w.sorted(func(p *simProc) bool { return p.carriesUUID() || p.outsider != "" })
}

func (w *world) advance(n int) {
	for i := 0; i < n; i++ {
		w.tick++
		if w.outsiderAt != 0 && w.tick >= w.outsiderAt {
			w.appear()
		}
		for _, p := range w.ours() {
			w.progress(p)
		}
		if n := len(w.ours()); n > 1 {
			w.violate("%d turn processes alive for one session", n)
		}
	}
}

// appear brings the outsider into the world. It never writes and never leaves:
// it only has to be there.
func (w *world) appear() {
	w.nextPID += 1 + w.rng.IntN(40)
	p := &simProc{pid: w.nextPID, start: w.tick, outsider: w.outsiderKind, dieAfter: -1, hangAfter: -1, regAt: 0}
	w.procs[p.pid] = p
	w.registry[p.pid] = p.start
	w.outsiders++
	w.outsiderAt = 0
}

func (w *world) progress(p *simProc) {
	off := w.tick - p.start
	if _, registered := w.registry[p.pid]; !registered && p.regAt >= 0 && off >= p.regAt {
		w.registry[p.pid] = p.start
	}
	for p.events < 3 && off >= p.writeAt+p.events {
		if p.dieAfter == p.events {
			w.end(p, false)
			return
		}
		if p.hangAfter == p.events {
			return
		}
		switch p.events {
		case 0:
			if !w.fileExists {
				w.fileExists = true
				w.sessionsCreated++
			}
		case 1:
			w.fileUUID = true
		case 2:
			w.filePrompt = true
		}
		p.events++
	}
	if p.events == 3 && off >= p.writeAt+2+p.exitGap {
		w.end(p, true)
	}
}

// end removes a process. A clean exit removes its registry entry; a death
// (SIGKILL, crash) leaves it behind. The PID may be taken over at once by an
// unrelated process that started later.
func (w *world) end(p *simProc, clean bool) {
	delete(w.procs, p.pid)
	if clean {
		delete(w.registry, p.pid)
	}
	if w.rng.Float64() < w.beh.pPIDReuse {
		w.procs[p.pid] = &simProc{pid: p.pid, start: w.tick, dieAfter: -1, hangAfter: -1, regAt: -1}
		w.pidReuseCreated++
	}
}

// spawn starts a turn process the way the adapter would, and returns the
// identity a caller reads back from the process table. Starting --session-id
// on an existing session, --resume on a missing one, or any process beside a
// process that works on the session, is a violation the model records.
func (w *world) spawn(kind string) Process {
	w.nextPID += 1 + w.rng.IntN(40)
	p := &simProc{pid: w.nextPID, start: w.tick, session: true, kind: kind, dieAfter: -1, hangAfter: -1}
	started := Process{PID: p.pid, StartUnix: unixOfTick(w.tick)}
	switch {
	case kind == "new" && w.fileExists:
		w.violate("--session-id used although the session file exists (Session ID is already in use)")
		return started
	case kind == "resume" && !w.fileExists:
		w.violate("--resume used although there is no session file")
		return started
	}
	if n := len(w.writers()); n > 0 {
		w.violate("a turn process was started beside %d process(es) that work on the session", n)
	}
	w.spawns++
	p.writeAt = 2 + w.rng.IntN(3)
	p.exitGap = 1 + w.rng.IntN(8)
	switch r := w.rng.Float64(); {
	case r < w.beh.pHang:
		p.hangAfter = w.rng.IntN(3)
	case r < w.beh.pHang+w.beh.pDeath:
		p.dieAfter = w.rng.IntN(3)
	case r < w.beh.pHang+w.beh.pDeath+w.beh.pSlow:
		p.writeAt = 130 + w.rng.IntN(50) // past the start timeout, then writes
	}
	switch r := w.rng.Float64(); {
	case r < w.beh.pRegNever:
		p.regAt = -1
		w.regNever++
	case r < w.beh.pRegNever+w.beh.pRegLate:
		p.regAt = 1 + w.rng.IntN(6)
		w.regLate++
	}
	if p.regAt == 0 {
		w.registry[p.pid] = p.start
	}
	w.procs[p.pid] = p
	if w.spawns == 1 && w.rng.Float64() < w.beh.pOutside {
		w.outsiderAt = w.tick + 1
		w.outsiderKind = "argv"
		if w.rng.Float64() < w.beh.pOutsideRegistry {
			w.outsiderKind = "registry"
		}
	}
	return started
}

func (w *world) lookup(pid int) Lookup {
	if p, ok := w.procs[pid]; ok {
		return Lookup{PID: pid, Alive: true, StartUnix: unixOfTick(p.start)}
	}
	return Lookup{PID: pid}
}

func (w *world) observeProcesses(s State) (recorded Lookup, running []Process, registry []RegistryEntry) {
	if !s.Process.IsZero() {
		recorded = w.lookup(s.Process.PID)
	}
	for _, p := range w.carrying() {
		running = append(running, Process{PID: p.pid, StartUnix: unixOfTick(p.start)})
	}
	pids := make([]int, 0, len(w.registry))
	for pid := range w.registry {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		registry = append(registry, RegistryEntry{
			SessionID: testSession, PID: pid, StartUnix: unixOfTick(w.registry[pid]), Current: w.lookup(pid),
		})
	}
	return
}

func (w *world) observeRecord() SessionRecord {
	return SessionRecord{Dir: testDir, Exists: w.fileExists, HasSessionID: w.fileUUID, HasFirstPrompt: w.filePrompt}
}

// truth is what the process table held when it was read, as the model knows it.
type truth struct {
	carrying     []*simProc // the uuid is on the command line
	registryOnly []*simProc // alive and registered, the uuid is not on the command line
}

func (w *world) truth() truth { return truth{carrying: w.carrying(), registryOnly: w.registryOnly()} }

// controlUnknown is the model's own rule: a writer that only the registry
// shows, or more than one process with the uuid on its command line.
func (t truth) controlUnknown() bool { return len(t.registryOnly) > 0 || len(t.carrying) > 1 }

// everyone is every process that works on the session, ordered by PID.
func (t truth) everyone() []Process {
	var out []Process
	for _, p := range append(append([]*simProc{}, t.carrying...), t.registryOnly...) {
		out = append(out, Process{PID: p.pid, StartUnix: unixOfTick(p.start)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// crashPlan says whether the adapter is killed at the idx-th crash point.
type crashPlan interface{ crashAt(idx int) bool }

type noCrash struct{}

func (noCrash) crashAt(int) bool { return false }

type crashOnce int

func (c crashOnce) crashAt(idx int) bool { return idx == int(c) }

type crashRandom struct{ rng *rand.Rand }

func (c crashRandom) crashAt(int) bool { return c.rng.Float64() < 0.25 }

// stats aggregates what the property test exercised, over all runs.
type stats struct {
	crashPoints map[string]int // crash points offered
	crashes     map[string]int // crash points where the adapter was killed
	actions     map[Action]int
	reasons     map[Reason]int
	spawnKinds  map[string]int
	outcomes    map[string]int
	recordedPIDReused,
	runsWithViolations int
	regLate, regNever int // turn processes whose registry entry was late or missing
	outsiderKinds     map[string]int
	// After a failure the world keeps moving and the adapter keeps deciding.
	afterFailureDecisions int
	promptAfterFailure    int // runs in which the first prompt reached the record after the failure
	idleAfterFailure      int // decisions after a failure with nothing running and no prompt: a start would be chosen
	evidenceAfterFailure  int // decisions after a failure with the prompt in the record: ready would be chosen
	runningAfterFailure   int // decisions after a failure with a process alive
}

func newStats() *stats {
	return &stats{
		crashPoints: map[string]int{}, crashes: map[string]int{}, actions: map[Action]int{},
		reasons: map[Reason]int{}, spawnKinds: map[string]int{}, outcomes: map[string]int{},
		outsiderKinds: map[string]int{},
	}
}

type runResult struct {
	outcome    string
	violations []string
	points     int // crash points offered in this run
}

const maxCycles = 700

// afterFailureCycles is how long the model keeps going after a failure: long
// enough for a process that timed out to write the first prompt.
const afterFailureCycles = 70

// keepGoingAfterFailure plays the adapter on after the failure was committed:
// the world keeps moving, the adapter restarts and decides again and again. A
// failed attempt answers done with the stored reason every time; it starts no
// process, records none and never becomes ready, whatever the world shows.
func keepGoingAfterFailure(w *world, db State, p Params, st *stats) {
	stored := db
	spawnsBefore := w.spawns
	promptBefore := w.filePrompt
	for cycle := 0; cycle < afterFailureCycles; cycle++ {
		w.advance(1 + w.rng.IntN(4))
		recorded, running, registry := w.observeProcesses(db)
		tr := w.truth()
		rec := w.observeRecord()
		d, err := Decide(db, Observations{Recorded: recorded, Running: running, Registry: registry, Record: rec}, p, w.time())
		if err != nil {
			w.violate("Decide refused a valid snapshot after the failure: %v", err)
			return
		}
		st.afterFailureDecisions++
		alive := len(tr.carrying)+len(tr.registryOnly) > 0
		switch {
		case alive:
			st.runningAfterFailure++
		case !rec.HasFirstPrompt:
			st.idleAfterFailure++
		}
		if rec.HasFirstPrompt {
			st.evidenceAfterFailure++
		}
		if d.Action != ActionDone || d.Reason != ReasonAlreadyFailed || d.FailReason != stored.FailReason {
			w.violate("after the failure (%v) Decide answered %v/%v with stored reason %v", stored.FailReason, d.Action, d.Reason, d.FailReason)
		}
		if db != stored {
			w.violate("the state changed after the failure: %+v", db)
		}
	}
	if w.spawns != spawnsBefore {
		w.violate("a process was started after the failure")
	}
	if !promptBefore && w.filePrompt {
		st.promptAfterFailure++
	}
}

// runAttempt plays one adapter through one creation attempt in a fresh world.
func runAttempt(seed uint64, b behaviour, plan crashPlan, st *stats) runResult {
	w := newWorld(seed, b)
	p := DefaultParams()
	db, err := NewIntent("attempt-1", testSession, testPrompt, testDir)
	if err != nil {
		panic(err)
	}
	points := 0
	crashed := false
	// point offers a crash point; true means the adapter is killed here.
	point := func(name string) bool {
		st.crashPoints[name]++
		idx := points
		points++
		if plan.crashAt(idx) {
			st.crashes[name]++
			return true
		}
		return false
	}
	commit := func(next State, err error) {
		if err != nil {
			w.violate("transition refused: %v", err)
			return
		}
		db = next
	}
	finish := func(outcome string) runResult {
		if w.sessionsCreated > 1 {
			w.violate("session created %d times", w.sessionsCreated)
		}
		st.outcomes[outcome]++
		st.regLate += w.regLate
		st.regNever += w.regNever
		if w.outsiders > 0 {
			st.outsiderKinds[w.outsiderKind]++
		}
		if len(w.violations) > 0 {
			st.runsWithViolations++
		}
		return runResult{outcome: outcome, violations: w.violations, points: points}
	}

	for cycle := 0; cycle < maxCycles; cycle++ {
		if crashed {
			w.advance(1 + w.rng.IntN(6)) // the adapter is down while the world goes on
			crashed = false
		}
		// Processes are observed before the session record; the world may move
		// on in between.
		recorded, running, registry := w.observeProcesses(db)
		tr := w.truth() // what was truly there when the process table was read
		if w.rng.Float64() < 0.3 {
			w.advance(1)
		}
		rec := w.observeRecord()
		if !db.Process.IsZero() && recorded.Alive && recorded.StartUnix != db.Process.StartUnix {
			st.recordedPIDReused++
		}
		d, err := Decide(db, Observations{Recorded: recorded, Running: running, Registry: registry, Record: rec}, p, w.time())
		if err != nil {
			w.violate("Decide refused a valid snapshot: %v", err)
			return finish("error")
		}
		st.actions[d.Action]++
		st.reasons[d.Reason]++
		name := d.Action.String()

		// A second writer, or one that only the registry shows, is control
		// unknown whatever else the world says; and nothing else is.
		if d.Action != ActionDone && tr.controlUnknown() != (d.Action == ActionControlUnknown) {
			w.violate("%d process(es) carry the uuid and %d only the registry shows, yet the decision is %v/%v",
				len(tr.carrying), len(tr.registryOnly), d.Action, d.Reason)
		}

		switch d.Action {
		case ActionStartNew, ActionStartResume:
			kind := map[Action]string{ActionStartNew: "new", ActionStartResume: "resume"}[d.Action]
			if point("pre:" + name) {
				crashed = true
				continue
			}
			st.spawnKinds[kind]++
			started := w.spawn(kind)
			if point("mid:" + name) { // started, PID not recorded
				crashed = true
				continue
			}
			commit(db.Started(started))
			if point("post:" + name) {
				crashed = true
				continue
			}
		case ActionRecordProcess:
			found := false
			for _, sp := range tr.carrying {
				if sp.pid == d.Process.PID && unixOfTick(sp.start) == d.Process.StartUnix {
					found = d.Process.PID != db.Process.PID || d.Process.StartUnix != db.Process.StartUnix
				}
			}
			if !found || len(tr.carrying) != 1 || len(tr.registryOnly) != 0 {
				w.violate("recorded a process that is not the one new running turn process: %+v", d.Process)
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
			commit(db.Started(d.Process))
			if point("post:" + name) {
				crashed = true
				continue
			}
		case ActionMarkUUIDSeen:
			if !w.fileUUID {
				w.violate("uuid marked seen but the session file lacks it")
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
			commit(db.UUIDSeen())
			if point("post:" + name) {
				crashed = true
				continue
			}
		case ActionMarkReady:
			if !w.filePrompt {
				w.violate("marked ready but the first prompt is not in the session record")
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
			commit(db.Ready())
			if point("post:" + name) {
				crashed = true
				continue
			}
		case ActionWait:
			if len(tr.carrying) != 1 {
				w.violate("waiting although %d turn process(es) ran when the process table was read", len(tr.carrying))
			} else if want := unixOfTick(tr.carrying[0].start) + int64(p.StartTimeout/time.Second); d.RecheckBy != want {
				w.violate("wait asks to decide again by %d, the timeout of the process falls at %d", d.RecheckBy, want)
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
		case ActionDone:
			switch db.Stage {
			case StageReady:
				if !w.filePrompt {
					w.violate("done without a ready session")
				}
				return finish("ready")
			case StageFailed:
				// The failure was committed and the adapter came back (or it
				// continues in the same breath): the answer is the stored reason.
				if d.FailReason != db.FailReason {
					w.violate("done with reason %v, the attempt failed with %v", d.FailReason, db.FailReason)
				}
				keepGoingAfterFailure(w, db, p, st)
				return finish(failureOutcome(db.FailReason))
			default:
				w.violate("done although the attempt is not final (stage %d)", db.Stage)
				return finish("error")
			}
		case ActionFail:
			switch d.Reason {
			case ReasonStartTimedOut:
				ok := false
				for _, sp := range tr.carrying {
					if w.tick-sp.start > int(p.StartTimeout/time.Second) {
						ok = true
					}
				}
				if !ok || w.filePrompt {
					w.violate("start timeout declared without an old running process, or with the prompt recorded")
				}
			case ReasonStartsExhausted:
				if w.spawns+w.outsiders < p.MaxStarts || len(tr.carrying) > 0 || w.filePrompt {
					w.violate("starts exhausted after %d spawns, %d running, prompt %v", w.spawns, len(tr.carrying), w.filePrompt)
				}
			default:
				w.violate("unexpected failure reason %v", d.Reason)
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
			commit(db.Failed(d.Reason))
			if point("post:" + name) {
				crashed = true
				continue
			}
		case ActionControlUnknown:
			if !reflect.DeepEqual(d.Processes, tr.everyone()) {
				w.violate("control unknown lists %+v, the processes at work on the session are %+v", d.Processes, tr.everyone())
			}
			wantReason := ReasonMultipleProcesses
			if len(tr.registryOnly) > 0 {
				wantReason = ReasonOutsideWriter
			}
			if d.Reason != wantReason {
				w.violate("control unknown with reason %v, want %v", d.Reason, wantReason)
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
			commit(db.Failed(d.Reason))
			if point("post:" + name) {
				crashed = true
				continue
			}
		default:
			w.violate("unexpected action %v", d.Action)
			return finish("error")
		}
		w.advance(1 + w.rng.IntN(3))
	}
	w.violate("the attempt did not settle within %d cycles", maxCycles)
	return finish("unsettled")
}

func failureOutcome(r Reason) string {
	if r == ReasonMultipleProcesses || r == ReasonOutsideWriter {
		return "controlunknown:" + r.String()
	}
	return "failed:" + r.String()
}

type scenario struct {
	name string
	beh  behaviour
	// mustReady: nothing in the scenario can stop the session from becoming
	// ready, so every run has to end ready.
	mustReady bool
	// mustEnd: every run has to end with this outcome.
	mustEnd string
}

var scenarios = []scenario{
	{name: "benign", mustReady: true},
	{name: "processes die part-way", beh: behaviour{pDeath: 0.5}},
	{name: "processes die, PIDs are reused", beh: behaviour{pDeath: 0.5, pPIDReuse: 1}},
	{name: "registry entries written late or never", beh: behaviour{pRegLate: 0.5, pRegNever: 0.4}, mustReady: true},
	{name: "PIDs reused after every end", beh: behaviour{pPIDReuse: 1}, mustReady: true},
	{name: "processes hang", beh: behaviour{pHang: 0.5, pDeath: 0.2}},
	{name: "processes start slowly and write the prompt after the timeout", beh: behaviour{pSlow: 0.6, pDeath: 0.1}},
	{name: "an outside process carries the uuid", beh: behaviour{pOutside: 1}},
	{name: "an interactive resume shows only in the registry", beh: behaviour{pOutside: 1, pOutsideRegistry: 1}, mustEnd: "controlunknown:OutsideWriter"},
	{name: "everything at once", beh: behaviour{pDeath: 0.3, pHang: 0.1, pSlow: 0.1, pRegLate: 0.3, pRegNever: 0.2, pPIDReuse: 0.5, pOutside: 0.3, pOutsideRegistry: 0.5}},
}

// TestEveryCrashPointKeepsOneProcessAndOneSession kills the adapter at every
// crash point of every scenario, one at a time, and at random combinations of
// them, and restarts it. In no run may two turn processes run for the session,
// the session be created twice, --session-id be used on an existing session,
// --resume on a missing one, a process be started beside another writer, a
// second writer go unnoticed, or a decision be unjustified by the world; and a
// failed attempt never starts or becomes ready afterwards.
func TestEveryCrashPointKeepsOneProcessAndOneSession(t *testing.T) {
	st := newStats()
	runs := 0
	check := func(label string, r runResult, sc scenario) {
		runs++
		for _, v := range r.violations {
			t.Errorf("%s: %s", label, v)
		}
		if sc.mustReady && r.outcome != "ready" {
			t.Errorf("%s: outcome %s, want ready", label, r.outcome)
		}
		if sc.mustEnd != "" && r.outcome != sc.mustEnd {
			t.Errorf("%s: outcome %s, want %s", label, r.outcome, sc.mustEnd)
		}
	}
	for si, sc := range scenarios {
		for seed := uint64(1); seed <= 12; seed++ {
			s := seed*100 + uint64(si)
			base := runAttempt(s, sc.beh, noCrash{}, st)
			check(fmt.Sprintf("%s seed %d no crash", sc.name, seed), base, sc)
			for i := 0; i < base.points+6; i++ { // a few beyond the base run: crashed runs offer other points
				r := runAttempt(s, sc.beh, crashOnce(i), st)
				check(fmt.Sprintf("%s seed %d crash at %d", sc.name, seed, i), r, sc)
			}
			rng := rand.New(rand.NewPCG(s, 7))
			for k := 0; k < 8; k++ {
				r := runAttempt(s, sc.beh, crashRandom{rng}, st)
				check(fmt.Sprintf("%s seed %d random crashes %d", sc.name, seed, k), r, sc)
			}
		}
	}

	// Coverage: every crash point, every action and the paths that matter were exercised.
	for _, a := range []Action{ActionStartNew, ActionStartResume, ActionRecordProcess, ActionMarkUUIDSeen, ActionMarkReady, ActionFail, ActionControlUnknown} {
		for _, phase := range []string{"pre:", "post:"} {
			key := phase + a.String()
			if st.crashes[key] < 20 {
				t.Errorf("crash point %s killed the adapter %d times, want at least 20", key, st.crashes[key])
			}
		}
	}
	for _, a := range []Action{ActionStartNew, ActionStartResume} {
		key := "mid:" + a.String()
		if st.crashes[key] < 20 {
			t.Errorf("crash point %s (started, PID not recorded) killed the adapter %d times, want at least 20", key, st.crashes[key])
		}
	}
	if st.crashes["pre:Wait"] < 20 {
		t.Errorf("crash while waiting: %d times", st.crashes["pre:Wait"])
	}
	for _, a := range []Action{ActionStartNew, ActionStartResume, ActionWait, ActionRecordProcess, ActionMarkUUIDSeen, ActionMarkReady, ActionDone, ActionFail, ActionControlUnknown} {
		if st.actions[a] < 50 {
			t.Errorf("action %v decided %d times, want at least 50", a, st.actions[a])
		}
	}
	for _, r := range []Reason{ReasonNotStarted, ReasonProcessEndedWithoutSession, ReasonRecordWithoutFirstPrompt,
		ReasonTurnRunning, ReasonProcessFoundByCommandLine, ReasonSessionRecordHasID,
		ReasonFirstPromptRecorded, ReasonAlreadyReady, ReasonAlreadyFailed, ReasonStartTimedOut, ReasonStartsExhausted,
		ReasonMultipleProcesses, ReasonOutsideWriter} {
		if st.reasons[r] < 20 {
			t.Errorf("reason %v given %d times, want at least 20", r, st.reasons[r])
		}
	}
	if st.recordedPIDReused < 50 {
		t.Errorf("a recorded PID held by an unrelated process was observed %d times, want at least 50", st.recordedPIDReused)
	}
	if st.regLate < 50 || st.regNever < 50 {
		t.Errorf("turn processes with a late registry entry: %d, without any: %d, want at least 50 each", st.regLate, st.regNever)
	}
	if st.spawnKinds["new"] < 200 || st.spawnKinds["resume"] < 100 {
		t.Errorf("spawns: %v", st.spawnKinds)
	}
	for _, o := range []string{"ready", "failed:StartTimedOut", "failed:StartsExhausted", "controlunknown:MultipleProcesses", "controlunknown:OutsideWriter"} {
		if st.outcomes[o] < 50 {
			t.Errorf("outcome %s reached %d times, want at least 50", o, st.outcomes[o])
		}
	}
	if st.outsiderKinds["argv"] < 50 || st.outsiderKinds["registry"] < 50 {
		t.Errorf("outsiders that appeared: %v, want at least 50 of each kind", st.outsiderKinds)
	}
	if st.afterFailureDecisions < 5000 {
		t.Errorf("only %d decisions were taken after a failure", st.afterFailureDecisions)
	}
	if st.promptAfterFailure < 20 {
		t.Errorf("the first prompt reached the record after the failure in %d runs, want at least 20", st.promptAfterFailure)
	}
	if st.idleAfterFailure < 500 || st.evidenceAfterFailure < 500 || st.runningAfterFailure < 500 {
		t.Errorf("decisions after a failure: idle %d, prompt in the record %d, process alive %d, want at least 500 each",
			st.idleAfterFailure, st.evidenceAfterFailure, st.runningAfterFailure)
	}
	t.Logf("runs=%d crash points offered=%v", runs, st.crashPoints)
	t.Logf("crashes taken=%v", st.crashes)
	t.Logf("actions=%v reasons=%v spawns=%v outcomes=%v recordedPIDReused=%d", st.actions, st.reasons, st.spawnKinds, st.outcomes, st.recordedPIDReused)
	t.Logf("registry late=%d never=%d outsiders=%v", st.regLate, st.regNever, st.outsiderKinds)
	t.Logf("after failure: decisions=%d promptAfterFailure=%d idle=%d promptInRecord=%d processAlive=%d",
		st.afterFailureDecisions, st.promptAfterFailure, st.idleAfterFailure, st.evidenceAfterFailure, st.runningAfterFailure)
}
