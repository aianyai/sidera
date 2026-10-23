package create

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

// This file holds an independent model of the world the adapter lives in: a
// session file, a process table, the runtime's session registry and a clock
// that ticks in seconds. The model knows how a Claude turn process behaves
// (what it writes and when, how it can die or hang, that --session-id fails on
// an existing session and --resume on a missing one); it knows nothing about
// how Decide reasons. The driver below plays the adapter: observe, decide,
// act, commit, and be killed between any two of those steps.

// simProc is a process in the model's table. Session processes are Claude turn
// processes of the session under test; the others are unrelated processes
// that took over a PID the system reused.
type simProc struct {
	pid, start int // start is a tick
	session    bool
	hidden     bool // the process table cannot read its command line
	kind       string
	writeAt    int // offset of the first write
	events     int // writes done: 0 create file, 1 write uuid, 2 write first prompt
	dieAfter   int // die when about to do this write; -1 never
	hangAfter  int // stop progressing when about to do this write; -1 never
	exitGap    int // ticks after the last write before a normal exit
}

type behaviour struct {
	pDeath, pHang, pHide, pPIDReuse float64
}

type world struct {
	rng  *rand.Rand
	beh  behaviour
	tick int

	fileExists, fileUUID, filePrompt bool
	sessionsCreated                  int

	procs    map[int]*simProc
	registry map[int]int // pid -> start tick, of the session processes that registered
	nextPID  int

	spawns          int // successful spawns
	violations      []string
	pidReuseCreated int
}

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

func (w *world) aliveSession() []*simProc {
	var out []*simProc
	for _, p := range w.procs {
		if p.session {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

func (w *world) advance(n int) {
	for i := 0; i < n; i++ {
		w.tick++
		for _, p := range w.aliveSession() {
			w.progress(p)
		}
		if n := len(w.aliveSession()); n > 1 {
			w.violate("%d turn processes alive for one session", n)
		}
	}
}

func (w *world) progress(p *simProc) {
	off := w.tick - p.start
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
		w.procs[p.pid] = &simProc{pid: p.pid, start: w.tick, dieAfter: -1, hangAfter: -1}
		w.pidReuseCreated++
	}
}

// spawn starts a turn process the way the adapter would, and returns the
// identity a caller reads back from the process table. Starting --session-id
// on an existing session, --resume on a missing one, or any process beside a
// running one, is a violation the model records.
func (w *world) spawn(kind string) Process {
	w.nextPID += 1 + w.rng.IntN(40)
	p := &simProc{pid: w.nextPID, start: w.tick, session: true, kind: kind, dieAfter: -1, hangAfter: -1}
	switch {
	case kind == "new" && w.fileExists:
		w.violate("--session-id used although the session file exists (Session ID is already in use)")
		return Process{PID: p.pid, StartTime: w.time()}
	case kind == "resume" && !w.fileExists:
		w.violate("--resume used although there is no session file")
		return Process{PID: p.pid, StartTime: w.time()}
	}
	if n := len(w.aliveSession()); n > 0 {
		w.violate("a turn process was started beside %d running one(s)", n)
	}
	w.spawns++
	p.writeAt = 2 + w.rng.IntN(3)
	p.exitGap = 1 + w.rng.IntN(8)
	p.hidden = w.rng.Float64() < w.beh.pHide
	switch r := w.rng.Float64(); {
	case r < w.beh.pHang:
		p.hangAfter = w.rng.IntN(3)
	case r < w.beh.pHang+w.beh.pDeath:
		p.dieAfter = w.rng.IntN(3)
	}
	w.procs[p.pid] = p
	w.registry[p.pid] = p.start
	return Process{PID: p.pid, StartTime: w.time()}
}

func (w *world) startOf(p *simProc) time.Time { return t0.Add(time.Duration(p.start) * time.Second) }

func (w *world) lookup(pid int) Lookup {
	if p, ok := w.procs[pid]; ok {
		return Lookup{Alive: true, StartTime: w.startOf(p)}
	}
	return Lookup{}
}

func (w *world) observeProcesses(s State) (recorded Lookup, running []Process, registry []RegistryEntry) {
	if !s.Process.IsZero() {
		recorded = w.lookup(s.Process.PID)
	}
	for _, p := range w.aliveSession() {
		if !p.hidden {
			running = append(running, Process{PID: p.pid, StartTime: w.startOf(p)})
		}
	}
	pids := make([]int, 0, len(w.registry))
	for pid := range w.registry {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		registry = append(registry, RegistryEntry{
			SessionID: testSession, PID: pid,
			StartTime: t0.Add(time.Duration(w.registry[pid]) * time.Second), Current: w.lookup(pid),
		})
	}
	return
}

func (w *world) observeRecord() SessionRecord {
	return SessionRecord{Dir: testDir, Exists: w.fileExists, HasSessionID: w.fileUUID, HasFirstPrompt: w.filePrompt}
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
}

func newStats() *stats {
	return &stats{
		crashPoints: map[string]int{}, crashes: map[string]int{}, actions: map[Action]int{},
		reasons: map[Reason]int{}, spawnKinds: map[string]int{}, outcomes: map[string]int{},
	}
}

type runResult struct {
	outcome    string
	violations []string
	points     int // crash points offered in this run
}

const maxCycles = 700

// runAttempt plays one adapter through one creation attempt in a fresh world.
func runAttempt(seed uint64, b behaviour, plan crashPlan, st *stats) runResult {
	w := newWorld(seed, b)
	p := DefaultParams()
	db, err := NewIntent("attempt-1", testSession, testDir)
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
		snapshot := w.aliveSession() // what was truly running when the table was read
		if w.rng.Float64() < 0.3 {
			w.advance(1)
		}
		rec := w.observeRecord()
		if !db.Process.IsZero() && recorded.Alive && !recorded.StartTime.Equal(db.Process.StartTime) {
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
			for _, sp := range snapshot {
				if sp.pid == d.Process.PID && w.startOf(sp).Unix() == d.Process.StartTime.Unix() {
					found = !d.Process.Same(db.Process)
				}
			}
			if !found {
				w.violate("recorded a process that is not a new running turn process: %+v", d.Process)
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
			if len(snapshot) == 0 {
				w.violate("waiting although no turn process ran when the process table was read")
			}
			if point("pre:" + name) {
				crashed = true
				continue
			}
		case ActionDone:
			if db.Stage != StageReady || !w.filePrompt {
				w.violate("done without a ready session")
			}
			return finish("ready")
		case ActionFail:
			switch d.Reason {
			case ReasonStartTimedOut:
				ok := false
				for _, sp := range snapshot {
					if w.tick-sp.start > int(p.StartTimeout/time.Second) {
						ok = true
					}
				}
				if !ok || w.filePrompt {
					w.violate("start timeout declared without an old running process, or with the prompt recorded")
				}
			case ReasonStartsExhausted:
				if w.spawns < p.MaxStarts || len(snapshot) > 0 || w.filePrompt {
					w.violate("starts exhausted after %d spawns, %d running, prompt %v", w.spawns, len(snapshot), w.filePrompt)
				}
			default:
				w.violate("unexpected failure reason %v", d.Reason)
			}
			return finish("failed:" + d.Reason.String())
		default:
			w.violate("unexpected action %v", d.Action)
			return finish("error")
		}
		w.advance(1 + w.rng.IntN(3))
	}
	w.violate("the attempt did not settle within %d cycles", maxCycles)
	return finish("unsettled")
}

type scenario struct {
	name string
	beh  behaviour
	// mustReady: nothing in the scenario can stop the session from becoming
	// ready, so every run has to end ready.
	mustReady bool
}

var scenarios = []scenario{
	{"benign", behaviour{}, true},
	{"processes die part-way", behaviour{pDeath: 0.5}, false},
	{"processes die, PIDs are reused", behaviour{pDeath: 0.5, pPIDReuse: 1}, false},
	{"command lines unreadable, only the registry shows processes", behaviour{pHide: 0.8}, true},
	{"PIDs reused after every end", behaviour{pPIDReuse: 1}, true},
	{"processes hang", behaviour{pHang: 0.5, pDeath: 0.2}, false},
	{"everything at once", behaviour{pDeath: 0.3, pHang: 0.1, pHide: 0.5, pPIDReuse: 0.5}, false},
}

// TestEveryCrashPointKeepsOneProcessAndOneSession kills the adapter at every
// crash point of every scenario, one at a time, and at random combinations of
// them, and restarts it. In no run may two turn processes run for the session,
// the session be created twice, --session-id be used on an existing session,
// --resume on a missing one, or a decision be unjustified by the world.
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
	for _, a := range []Action{ActionStartNew, ActionStartResume, ActionRecordProcess, ActionMarkUUIDSeen, ActionMarkReady} {
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
	for _, a := range []Action{ActionStartNew, ActionStartResume, ActionWait, ActionRecordProcess, ActionMarkUUIDSeen, ActionMarkReady, ActionDone, ActionFail} {
		if st.actions[a] < 50 {
			t.Errorf("action %v decided %d times, want at least 50", a, st.actions[a])
		}
	}
	for _, r := range []Reason{ReasonNotStarted, ReasonProcessEndedWithoutSession, ReasonRecordWithoutFirstPrompt,
		ReasonTurnRunning, ReasonProcessFoundByCommandLine, ReasonProcessFoundInRegistry, ReasonSessionRecordHasID,
		ReasonFirstPromptRecorded, ReasonAlreadyReady, ReasonStartTimedOut, ReasonStartsExhausted} {
		if st.reasons[r] < 20 {
			t.Errorf("reason %v given %d times, want at least 20", r, st.reasons[r])
		}
	}
	if st.reasons[ReasonMultipleProcesses] != 0 {
		t.Errorf("the model never runs two processes, yet ReasonMultipleProcesses was given %d times", st.reasons[ReasonMultipleProcesses])
	}
	if st.recordedPIDReused < 50 {
		t.Errorf("a recorded PID held by an unrelated process was observed %d times, want at least 50", st.recordedPIDReused)
	}
	if st.spawnKinds["new"] < 200 || st.spawnKinds["resume"] < 100 {
		t.Errorf("spawns: %v", st.spawnKinds)
	}
	for _, o := range []string{"ready", "failed:StartTimedOut", "failed:StartsExhausted"} {
		if st.outcomes[o] < 50 {
			t.Errorf("outcome %s reached %d times, want at least 50", o, st.outcomes[o])
		}
	}
	t.Logf("runs=%d crash points offered=%v", runs, st.crashPoints)
	t.Logf("crashes taken=%v", st.crashes)
	t.Logf("actions=%v reasons=%v spawns=%v outcomes=%v recordedPIDReused=%d", st.actions, st.reasons, st.spawnKinds, st.outcomes, st.recordedPIDReused)
}
