package scenario_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/testkit/crashwalk"
	"github.com/aianyai/sidera/internal/node/testkit/fakeadapter"
	"github.com/aianyai/sidera/internal/node/testkit/fakeagent"
	"github.com/aianyai/sidera/internal/node/testkit/fakeruntime"
	"github.com/aianyai/sidera/internal/node/testkit/ledger"
	"github.com/aianyai/sidera/internal/node/testkit/scenario"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// recEnv is an Env that does nothing but write down what the driver asks of it,
// so that a test of the driver needs no system under test.
type recEnv struct {
	calls      []string
	failDirect string
	quiesceErr map[int]error // by the ordinal of the call, from 1
	quiesces   int
	led        *ledger.Memory
	agents     map[string]fakeagent.Agent
}

func newRecEnv() *recEnv {
	return &recEnv{led: ledger.NewMemory(), quiesceErr: map[int]error{}, agents: map[string]fakeagent.Agent{}}
}

func (e *recEnv) note(format string, args ...any) {
	e.calls = append(e.calls, fmt.Sprintf(format, args...))
}

func (e *recEnv) Ctx() context.Context { return context.Background() }
func (e *recEnv) Agent(name string) fakeagent.Agent {
	return e.agents[name]
}
func (e *recEnv) Addr(name string) string { return "addr-of-" + name }
func (e *recEnv) Direct(name string) (string, error) {
	e.note("direct %s", name)
	if name == e.failDirect {
		return "", errors.New("no such adapter")
	}
	return "addr-of-" + name, nil
}
func (e *recEnv) Advance(d time.Duration) { e.note("advance %s", d) }
func (e *recEnv) Set(t time.Time)         { e.note("set %s", t.UTC().Format(time.RFC3339)) }
func (e *recEnv) Quiesce(...scenario.QuiesceOption) error {
	e.quiesces++
	e.note("quiesce")
	return e.quiesceErr[e.quiesces]
}
func (e *recEnv) BeginStep(name string) error { e.note("begin %s", name); return nil }
func (e *recEnv) Adapter(string) (fakeadapter.Controller, error) {
	return nil, scenario.NotAvailable("an adapter host")
}
func (e *recEnv) Runtime() (fakeruntime.Controller, error) {
	return nil, scenario.NotAvailable("a runtime")
}
func (e *recEnv) Ledger() (*ledger.Log, error) { return e.led.Log() }

var _ scenario.Env = (*recEnv)(nil)

func step(name string, e *recEnv) scenario.Step {
	return scenario.Step{Name: name, Do: func(scenario.Env) error { e.note("do %s", name); return nil }}
}

func TestRunSetsUpDirectEndpointsThenRunsEachStepAndQuiescesAfterIt(t *testing.T) {
	e := newRecEnv()
	s := scenario.Script{
		Name:  "order",
		Setup: scenario.Setup{Direct: []string{"a", "b"}},
		Steps: []scenario.Step{step("one", e), step("two", e)},
	}
	if err := scenario.Run(e, s); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"direct a", "direct b", "quiesce",
		"begin one", "do one", "quiesce",
		"begin two", "do two", "quiesce",
	}
	if !reflect.DeepEqual(e.calls, want) {
		t.Errorf("the driver made these calls:\n%s\nwant:\n%s", strings.Join(e.calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestRunStopsAtTheFirstFailedStepAndNamesIt(t *testing.T) {
	e := newRecEnv()
	boom := errors.New("boom")
	s := scenario.Script{Name: "fails", Steps: []scenario.Step{
		step("one", e),
		{Name: "two", Do: func(scenario.Env) error { return boom }},
		step("three", e),
	}}
	err := scenario.Run(e, s)
	if !errors.Is(err, boom) {
		t.Fatalf("Run returned %v, want the step's own error", err)
	}
	if !strings.Contains(err.Error(), `"two"`) || !strings.Contains(err.Error(), "fails") {
		t.Errorf("error %q names neither the script nor the step", err)
	}
	for _, c := range e.calls {
		if c == "do three" || c == "begin three" {
			t.Errorf("the driver went on after a failed step: %s", c)
		}
	}
	if e.quiesces != 2 { // after the setup and after step one
		t.Errorf("%d quiesces, want 2: the setup, step one and not the failed step", e.quiesces)
	}
}

func TestRunReportsAFailureToGoQuietAndNamesTheStep(t *testing.T) {
	e := newRecEnv()
	cause := errors.New("not quiet: loop busy")
	e.quiesceErr[3] = cause // setup 1, step one 2, step two 3
	s := scenario.Script{Name: "busy", Steps: []scenario.Step{step("one", e), step("two", e), step("three", e)}}
	// the first quiesce is the one after the setup
	err := scenario.Run(e, s)
	if !errors.Is(err, cause) {
		t.Fatalf("Run returned %v, want the quiesce error", err)
	}
	if !strings.Contains(err.Error(), `"two"`) {
		t.Errorf("error %q does not name the step after which the system did not go quiet", err)
	}
	if slicesContain(e.calls, "begin three") {
		t.Error("the driver went on after the system failed to go quiet")
	}
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRunRefusesAScriptThatCannotRunBeforeTouchingTheSystem(t *testing.T) {
	e := newRecEnv()
	ok := func(scenario.Env) error { return nil }
	for _, tc := range []struct {
		name string
		s    scenario.Script
	}{
		{"a script has a name", scenario.Script{Steps: []scenario.Step{{Name: "x", Do: ok}}}},
		{"a step has a name", scenario.Script{Name: "s", Steps: []scenario.Step{{Do: ok}}}},
		{"a step has something to do", scenario.Script{Name: "s", Steps: []scenario.Step{{Name: "x"}}}},
		{"step names are unique, because an entry names its step and nothing else",
			scenario.Script{Name: "s", Steps: []scenario.Step{{Name: "x", Do: ok}, {Name: "x", Do: ok}}}},
		{"a direct endpoint has a name", scenario.Script{Name: "s", Setup: scenario.Setup{Direct: []string{""}}}},
		{"a direct endpoint is set up once", scenario.Script{Name: "s", Setup: scenario.Setup{Direct: []string{"a", "a"}}}},
		{"an unknown judgement cannot be waived", scenario.Script{Name: "s",
			Expect: scenario.Invariants{Waive: map[scenario.Judgement]string{"no-such-rule": "because"}}}},
		{"a waiver gives its reason", scenario.Script{Name: "s",
			Expect: scenario.Invariants{Waive: map[scenario.Judgement]string{scenario.JudgeConfirmations: ""}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := scenario.Run(e, tc.s); err == nil {
				t.Fatal("Run accepted the script")
			}
			if err := tc.s.Validate(); err == nil {
				t.Error("Validate accepted the script")
			}
			if len(e.calls) != 0 {
				t.Errorf("the driver touched the system before it refused the script: %v", e.calls)
			}
		})
	}
}

func TestRunStopsWhenADirectEndpointCannotBeSetUp(t *testing.T) {
	e := newRecEnv()
	e.failDirect = "b"
	s := scenario.Script{Name: "s", Setup: scenario.Setup{Direct: []string{"a", "b", "c"}}, Steps: []scenario.Step{step("one", e)}}
	err := scenario.Run(e, s)
	if err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("Run returned %v, want an error that names the endpoint b", err)
	}
	if slicesContain(e.calls, "direct c") || slicesContain(e.calls, "begin one") {
		t.Errorf("the driver went on after the setup failed: %v", e.calls)
	}
}

func TestAdvanceIsOneStepThatMovesTheClockByExactlyTheDuration(t *testing.T) {
	e := newRecEnv()
	st := scenario.Advance("deliver", 90*time.Second)
	if st.Name != "deliver" {
		t.Fatalf("step name %q", st.Name)
	}
	if err := scenario.Run(e, scenario.Script{Name: "s", Steps: []scenario.Step{st}}); err != nil {
		t.Fatal(err)
	}
	if !slicesContain(e.calls, "advance 1m30s") {
		t.Errorf("calls %v: the clock was not advanced by 1m30s", e.calls)
	}
	n := 0
	for _, c := range e.calls {
		if strings.HasPrefix(c, "advance") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d advances, want exactly one", n)
	}
}

// pageAgent hands out the pages it was given, one per Recv, then empty pages,
// and writes down every acknowledgement.
type pageAgent struct {
	fakeagent.Agent
	pages    []adapterproto.RecvPage
	recvs    int
	acked    []adapterproto.LeaseRef
	ackItems [][]adapterproto.PartRef
	ackErr   error
}

func (a *pageAgent) Recv(context.Context, fakeagent.RecvInput) (fakeagent.RecvResult, error) {
	a.recvs++
	if len(a.pages) == 0 {
		return fakeagent.RecvResult{Page: adapterproto.RecvPage{Lease: "lease-empty"}}, nil
	}
	p := a.pages[0]
	a.pages = a.pages[1:]
	return fakeagent.RecvResult{Page: p}, nil
}

func (a *pageAgent) Ack(_ context.Context, in fakeagent.AckInput) (fakeagent.AckResult, error) {
	a.acked = append(a.acked, in.Lease)
	a.ackItems = append(a.ackItems, in.Items)
	return fakeagent.AckResult{}, a.ackErr
}

func page(lease string, n int) adapterproto.RecvPage {
	p := adapterproto.RecvPage{Lease: adapterproto.LeaseRef(lease)}
	for i := 0; i < n; i++ {
		p.Messages = append(p.Messages, adapterproto.PageMessage{})
	}
	return p
}

func TestRecvAndAckAllAcknowledgesEveryPageAndStopsAtAnEmptyOne(t *testing.T) {
	e := newRecEnv()
	a := &pageAgent{pages: []adapterproto.RecvPage{page("l1", 2), page("l2", 1)}}
	e.agents["b"] = a
	if err := scenario.RecvAndAckAll("b")(e); err != nil {
		t.Fatal(err)
	}
	if a.recvs != 3 {
		t.Errorf("%d receives, want 3: two pages and the empty one that ends it", a.recvs)
	}
	if want := []adapterproto.LeaseRef{"l1", "l2"}; !reflect.DeepEqual(a.acked, want) {
		t.Errorf("acknowledged leases %v, want %v: the empty page has nothing to acknowledge", a.acked, want)
	}
	for i, items := range a.ackItems {
		if len(items) != 0 {
			t.Errorf("acknowledgement %d names %d parts, want none: no items means every part the lease handed out", i, len(items))
		}
	}
}

func TestRecvAndAckAllReturnsTheErrorOfTheCallThatFailed(t *testing.T) {
	e := newRecEnv()
	boom := &fakeagent.Error{Category: fakeagent.CategoryRefused, Code: "lease_unknown", Message: "gone"}
	a := &pageAgent{pages: []adapterproto.RecvPage{page("l1", 1)}, ackErr: boom}
	e.agents["b"] = a
	err := scenario.RecvAndAckAll("b")(e)
	if !errors.Is(err, boom) {
		t.Fatalf("returned %v, want the acknowledgement's error", err)
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("error %q does not name the endpoint", err)
	}
}

func TestAScriptIsAScenarioOfTheCrashWalk(t *testing.T) {
	recovery := []crashwalk.RecoveryCase{{
		Item:  fsx.Key{Tag: fsx.Tag{Tx: "orphan-gc", Role: "body"}, Step: fsx.StepRemove},
		After: crashwalk.CrashAt(fsx.Point{Key: fsx.Key{Tag: fsx.Tag{Tx: "intake", Role: "out"}, Step: fsx.StepWrite}, Hit: 1}),
	}}
	s := scenario.Script{
		Name:  "send-recv-ack",
		Steps: []scenario.Step{{Name: "a sends", Do: func(scenario.Env) error { return nil }}, scenario.Advance("deliver", time.Minute)},
		Crash: scenario.CrashPlan{MaxPoints: 60, Recovery: recovery},
	}
	var sc crashwalk.Scenario = s.Scenario()
	if sc.Name() != "send-recv-ack" {
		t.Errorf("Name %q", sc.Name())
	}
	if want := []string{"a sends", "deliver"}; !reflect.DeepEqual(sc.Steps(), want) {
		t.Errorf("Steps %v, want %v", sc.Steps(), want)
	}
	if sc.MaxPoints() != 60 {
		t.Errorf("MaxPoints %d", sc.MaxPoints())
	}
	if !reflect.DeepEqual(sc.Recovery(), recovery) {
		t.Errorf("Recovery %v, want %v", sc.Recovery(), recovery)
	}
}

func TestNotAvailableNamesWhatIsMissingAndIsTheSentinel(t *testing.T) {
	err := scenario.NotAvailable("an adapter host")
	if !errors.Is(err, scenario.ErrNotAvailable) {
		t.Fatalf("%v is not ErrNotAvailable", err)
	}
	for _, want := range []string{"an adapter host", "not available at this layer yet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
}

func TestQuiesceOptionsDefaultToTheLimitOfSixtySeconds(t *testing.T) {
	if got := scenario.ResolveQuiesce(nil).Timeout; got != 60*time.Second {
		t.Errorf("default limit %v, want 60s", got)
	}
	if got := scenario.ResolveQuiesce([]scenario.QuiesceOption{scenario.WithTimeout(time.Second)}).Timeout; got != time.Second {
		t.Errorf("limit %v, want 1s", got)
	}
	if got := scenario.ResolveQuiesce([]scenario.QuiesceOption{scenario.WithTimeout(0)}).Timeout; got != 60*time.Second {
		t.Errorf("limit %v for a zero timeout, want the default: zero means unset", got)
	}
}

// --- the judgements, over ledgers written out by hand ---

var epoch = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func now() time.Time { return epoch }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writer(t *testing.T, m *ledger.Memory, name string) *ledger.Writer {
	t.Helper()
	w, err := m.Open(name, 0, now)
	must(t, err)
	return w
}

func yes() *bool { b := true; return &b }

func digest(s string) string { return ledger.Digest([]byte(s)) }

func saw(msg string, part int, dup *bool) ledger.Attrs {
	return ledger.Attrs{Endpoint: "b", MessageID: msg, Part: part, BodyDigest: digest(msg), From: "a", PossiblyDuplicate: dup}
}

func TestCheckHoldsEveryRuleOfAnEmptyLedgerAndOfACorrectOne(t *testing.T) {
	m := ledger.NewMemory()
	log, err := m.Log()
	must(t, err)
	must(t, scenario.Check(log, scenario.Invariants{}))

	rt, ad, ag := writer(t, m, "runtime/n1"), writer(t, m, "adapter/chat"), writer(t, m, "agent/b")
	seq, err := rt.AppendSeq(ledger.KindRuntimeSessionCreated, ledger.Attrs{Attempt: "t1", NativeRef: "n1"})
	must(t, err)
	must(t, ad.Append(ledger.KindNativeCreateReturned, ledger.Attrs{Attempt: "t1", Outcome: "created", NativeRef: "n1", RuntimeWriter: "runtime/n1", RuntimeSeq: seq}))
	must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
	must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p2", TextDigest: digest("again")}))
	must(t, ag.Append(ledger.KindModelSaw, saw("m1", 0, nil)))
	must(t, ag.Append(ledger.KindModelSaw, saw("m1", 0, yes())))
	must(t, ag.Append(ledger.KindModelSaw, saw("m2", 1, nil)))
	must(t, ag.Append(ledger.KindModelSaw, saw("m2", 2, nil)))
	log, err = m.Log()
	must(t, err)
	must(t, scenario.Check(log, scenario.Invariants{}))
}

func TestCheckRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rule  scenario.Judgement
		write func(t *testing.T, m *ledger.Memory)
	}{
		{"a confirmation names an entry that the runtime never wrote", scenario.JudgeConfirmations, func(t *testing.T, m *ledger.Memory) {
			must(t, writer(t, m, "adapter/chat").Append(ledger.KindNativeCreateReturned, ledger.Attrs{
				Attempt: "t1", Outcome: "created", NativeRef: "n1", RuntimeWriter: "runtime/n1", RuntimeSeq: 9}))
		}},
		{"two native sessions for one creation attempt", scenario.JudgeOneSessionPerAttempt, func(t *testing.T, m *ledger.Memory) {
			must(t, writer(t, m, "runtime/n1").Append(ledger.KindRuntimeSessionCreated, ledger.Attrs{Attempt: "t1", NativeRef: "n1"}))
			must(t, writer(t, m, "runtime/n2").Append(ledger.KindRuntimeSessionCreated, ledger.Attrs{Attempt: "t1", NativeRef: "n2"}))
		}},
		{"a prompt attempt that reached the runtime twice", scenario.JudgePromptOncePerAttempt, func(t *testing.T, m *ledger.Memory) {
			rt := writer(t, m, "runtime/n1")
			must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
			must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
		}},
		{"a part shown to the model a second time without the possible-duplicate mark", scenario.JudgePossiblyDuplicateMarked, func(t *testing.T, m *ledger.Memory) {
			ag := writer(t, m, "agent/b")
			must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, nil)))
			must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, nil)))
		}},
		{"a part shown a third time, the second carried the mark and the third does not", scenario.JudgePossiblyDuplicateMarked, func(t *testing.T, m *ledger.Memory) {
			ag := writer(t, m, "agent/b")
			must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, nil)))
			must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, yes())))
			must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, nil)))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := ledger.NewMemory()
			tc.write(t, m)
			log, err := m.Log()
			must(t, err)
			err = scenario.Check(log, scenario.Invariants{})
			if err == nil {
				t.Fatal("Check accepted the ledger")
			}
			if !strings.Contains(err.Error(), string(tc.rule)) {
				t.Errorf("error %q does not name the judgement %s", err, tc.rule)
			}
			for _, other := range scenario.Judgements() {
				if other != tc.rule && strings.Contains(err.Error(), string(other)) {
					t.Errorf("error %q blames %s as well: the ledger breaks only %s", err, other, tc.rule)
				}
			}
			// A waiver, with its reason, is what lets a scenario that breaks the
			// rule on purpose go on.
			waive := scenario.Invariants{Waive: map[scenario.Judgement]string{tc.rule: "the scenario makes the case on purpose"}}
			if err := scenario.Check(log, waive); err != nil {
				t.Errorf("Check refused a waived judgement: %v", err)
			}
		})
	}
}

func TestCheckIsTheSameForEveryOrderOfTheJudgementsAndNamesEveryOneThatFails(t *testing.T) {
	m := ledger.NewMemory()
	rt := writer(t, m, "runtime/n1")
	must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
	must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
	ag := writer(t, m, "agent/b")
	must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, nil)))
	must(t, ag.Append(ledger.KindModelSaw, saw("m1", 1, nil)))
	log, err := m.Log()
	must(t, err)
	err = scenario.Check(log, scenario.Invariants{})
	if err == nil {
		t.Fatal("Check accepted the ledger")
	}
	for _, want := range []scenario.Judgement{scenario.JudgePromptOncePerAttempt, scenario.JudgePossiblyDuplicateMarked} {
		if !strings.Contains(err.Error(), string(want)) {
			t.Errorf("error %q does not name %s: every failing judgement is reported, not the first", err, want)
		}
	}
}

func TestJudgementsListsEachOnceAndEachCanBeWaived(t *testing.T) {
	seen := map[scenario.Judgement]bool{}
	for _, j := range scenario.Judgements() {
		if j == "" || seen[j] {
			t.Errorf("judgement %q is empty or listed twice", j)
		}
		seen[j] = true
		s := scenario.Script{Name: "s", Expect: scenario.Invariants{Waive: map[scenario.Judgement]string{j: "reason"}}}
		if err := s.Validate(); err != nil {
			t.Errorf("a waiver of %s is refused: %v", j, err)
		}
	}
	if len(seen) < 4 {
		t.Errorf("%d judgements, want at least the four that need only the ledger", len(seen))
	}
}

func TestRunJudgesTheLedgerAfterTheLastStep(t *testing.T) {
	e := newRecEnv()
	rt := writer(t, e.led, "runtime/n1")
	must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
	must(t, rt.Append(ledger.KindRuntimePromptReceived, ledger.Attrs{Attempt: "p1", TextDigest: digest("hi")}))
	s := scenario.Script{Name: "s", Steps: []scenario.Step{step("one", e)}}
	err := scenario.Run(e, s)
	if err == nil || !strings.Contains(err.Error(), string(scenario.JudgePromptOncePerAttempt)) {
		t.Fatalf("Run returned %v, want the broken judgement", err)
	}
	if !slicesContain(e.calls, "do one") {
		t.Error("the steps did not run before the ledger was judged")
	}
	s.Expect = scenario.Invariants{Waive: map[scenario.Judgement]string{scenario.JudgePromptOncePerAttempt: "made on purpose"}}
	if err := scenario.Run(newRecEnvWithLedger(t, e.led), s); err != nil {
		t.Fatalf("Run with the judgement waived: %v", err)
	}
}

func newRecEnvWithLedger(t *testing.T, l *ledger.Memory) *recEnv {
	t.Helper()
	e := newRecEnv()
	e.led = l
	return e
}
