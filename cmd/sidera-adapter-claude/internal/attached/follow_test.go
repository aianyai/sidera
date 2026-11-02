package attached

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aianyai/sidera/cmd/sidera-adapter-claude/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

const (
	attemptA = "11111111-1111-4111-8111-111111111111"
	attemptB = "22222222-2222-4222-8222-222222222222"
)

var t0 = time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)

// look is one look at the session: the registry (status and its time), the
// seconds since t0 and the markers of the transcript.
type look struct {
	status  Status
	at      int64
	sec     float64
	markers []record.Marker
	want    []string
}

var (
	promptA = record.Marker{Kind: record.MarkPrompt, UUID: attemptA}
	promptB = record.Marker{Kind: record.MarkPrompt, UUID: attemptB}
	turnEnd = record.Marker{Kind: record.MarkTurnEnd}
)

func describe(events []Event) []string {
	out := []string{}
	for _, e := range events {
		switch e.Kind {
		case EventPersisted:
			out = append(out, "persisted "+e.Attempt)
		case EventStarted:
			out = append(out, fmt.Sprintf("started %s attempt=%s", e.Turn, e.Attempt))
		case EventEnded:
			out = append(out, fmt.Sprintf("ended %s %s", e.Turn, e.Final))
		}
	}
	return out
}

func run(t *testing.T, f *Follower, looks []look) {
	t.Helper()
	for i, l := range looks {
		got := describe(f.Step(Observation{
			Status: l.status, StatusAt: l.at, Now: t0.Add(time.Duration(l.sec * float64(time.Second))), Markers: l.markers,
		}))
		if !slices.Equal(got, l.want) {
			t.Fatalf("look %d: events %q, want %q", i, got, l.want)
		}
	}
}

func TestATurnThePersonStartsIsToldFromTheRegistry(t *testing.T) {
	var f Follower
	run(t, &f, []look{
		{StatusIdle, 100, 0, nil, []string{}},
		{StatusBusy, 200, 1, nil, []string{"started person-200 attempt="}},
		{StatusWaiting, 250, 2, nil, []string{}}, // a permission prompt is still the turn
		{StatusBusy, 300, 3, nil, []string{}},
		{StatusIdle, 400, 4, nil, []string{}}, // the transcript has not written the end yet
		{StatusIdle, 400, 4.3, []record.Marker{turnEnd}, []string{"ended person-200 completed"}},
		{StatusIdle, 400, 9, nil, []string{}},
		{StatusBusy, 500, 10, nil, []string{"started person-500 attempt="}},
	})
}

func TestAStatusOfAnEndedTurnIsNotANewTurn(t *testing.T) {
	var f Follower
	run(t, &f, []look{
		{StatusBusy, 200, 0, nil, []string{"started person-200 attempt="}},
		// The end is read while the registry read still shows the old status.
		{StatusBusy, 200, 1, []record.Marker{turnEnd}, []string{"ended person-200 completed"}},
		{StatusBusy, 200, 1.2, nil, []string{}},
		{StatusIdle, 300, 1.4, nil, []string{}},
		// A later status is a new turn: a background task woke the session.
		{StatusBusy, 400, 5, nil, []string{"started person-400 attempt="}},
	})
}

func TestAPromptWakesAnIdleSession(t *testing.T) {
	var f Follower
	f.Expect(attemptA)
	run(t, &f, []look{
		{StatusIdle, 100, 0, nil, []string{}},
		// The record can be seen before the registry shows the turn.
		{StatusIdle, 100, 0.2, []record.Marker{promptA}, []string{"persisted " + attemptA, "started " + attemptA + " attempt=" + attemptA}},
		{StatusBusy, 200, 0.4, nil, []string{}},
		{StatusIdle, 300, 3, nil, []string{}},
		{StatusIdle, 300, 3.3, []record.Marker{turnEnd}, []string{"ended " + attemptA + " completed"}},
	})
	if len(f.Expected()) != 0 {
		t.Fatalf("expecting %v", f.Expected())
	}
}

func TestARegistryThatIsFirstDoesNotHideThePrompt(t *testing.T) {
	var f Follower
	f.Expect(attemptA)
	run(t, &f, []look{
		{StatusBusy, 200, 0, nil, []string{"started person-200 attempt="}},
		// The prompt's record comes after the registry showed a turn: the prompt is
		// delivered, and it is told so, but it started no turn of its own.
		{StatusBusy, 200, 0.2, []record.Marker{promptA}, []string{"persisted " + attemptA}},
		{StatusIdle, 300, 3, []record.Marker{turnEnd}, []string{"ended person-200 completed"}},
	})
}

func TestAPromptWrittenIntoARunningTurnIsAbsorbed(t *testing.T) {
	var f Follower
	f.Expect(attemptA)
	run(t, &f, []look{
		{StatusBusy, 200, 0, nil, []string{"started person-200 attempt="}},
		{StatusBusy, 200, 1, []record.Marker{promptA}, []string{"persisted " + attemptA}},
		{StatusBusy, 200, 2, []record.Marker{turnEnd}, []string{"ended person-200 completed"}},
	})
}

func TestAPromptOfAnotherSenderOrAReplayIsNotOurs(t *testing.T) {
	var f Follower
	f.Expect(attemptA)
	run(t, &f, []look{
		{StatusIdle, 100, 0, []record.Marker{promptB}, []string{}},
		{StatusIdle, 100, 1, []record.Marker{turnEnd}, []string{}}, // an end with no turn open
	})
	f.Forget(attemptA)
	run(t, &f, []look{{StatusIdle, 100, 2, []record.Marker{promptA}, []string{}}})
}

func TestATurnTheTranscriptNeverEndsEndsWhenTheRegistryHasBeenIdleLongEnough(t *testing.T) {
	var f Follower
	run(t, &f, []look{
		{StatusBusy, 200, 0, nil, []string{"started person-200 attempt="}},
		{StatusIdle, 300, 1, nil, []string{}},
		{StatusIdle, 300, 2.9, nil, []string{}},
		{StatusIdle, 300, 3, nil, []string{"ended person-200 interrupted"}},
		{StatusIdle, 300, 4, nil, []string{}},
	})

	// A turn that shows itself busy again before the grace is over goes on.
	var g Follower
	run(t, &g, []look{
		{StatusBusy, 200, 0, nil, []string{"started person-200 attempt="}},
		{StatusIdle, 300, 1, nil, []string{}},
		{StatusBusy, 350, 2, nil, []string{}},
		{StatusIdle, 400, 3, nil, []string{}},
		{StatusIdle, 400, 4.9, nil, []string{}},
		{StatusIdle, 400, 5.1, nil, []string{"ended person-200 interrupted"}},
	})
}

func TestAShellStatusIsATurnThatEnded(t *testing.T) {
	var f Follower
	run(t, &f, []look{
		{StatusBusy, 200, 0, nil, []string{"started person-200 attempt="}},
		{StatusShell, 300, 1, []record.Marker{turnEnd}, []string{"ended person-200 completed"}},
		{StatusShell, 300, 30, nil, []string{}},
	})
}

func TestAnUnknownStatusIsNoTurn(t *testing.T) {
	var f Follower
	run(t, &f, []look{{StatusUnknown, 200, 0, nil, []string{}}})
}

// A session that stays busy from one turn to the next keeps its status and the time
// of it, so the registry shows no new turn. The user records that begin the turns
// do, and a message that arrives in a turn that runs begins none.
func TestBackToBackTurnsAreToldFromTheTranscript(t *testing.T) {
	ix := record.NewIndex(session)
	ix.Track()
	ix.Feed(read(t, "transcript-back-to-back.jsonl"))
	m := ix.Markers()
	if len(m) != 5 {
		t.Fatalf("markers %v", m)
	}
	first, second := "person-aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "person-cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	var f Follower
	run(t, &f, []look{
		{StatusBusy, 200, 0, m[:1], []string{"started " + first + " attempt="}},
		{StatusBusy, 200, 1, m[1:2], []string{"ended " + first + " completed"}},
		// The status is still the first turn's: it opens nothing, and the next records do.
		{StatusBusy, 200, 2, m[2:4], []string{"started " + second + " attempt="}}, // the person's message in the turn begins none
		{StatusBusy, 200, 3, m[4:], []string{"ended " + second + " completed"}},
		{StatusIdle, 200, 4, nil, []string{}},
	})
}

// What the transcript still shows when the session goes is told before the turn
// ends with it.
func TestFinishTellsTheMarkersFirst(t *testing.T) {
	var f Follower
	f.Expect(attemptA)
	run(t, &f, []look{{StatusIdle, 100, 0, nil, []string{}}})
	if got := describe(f.Finish([]record.Marker{promptA, turnEnd}, adapterproto.TurnInterrupted)); !slices.Equal(got,
		[]string{"persisted " + attemptA, "started " + attemptA + " attempt=" + attemptA, "ended " + attemptA + " completed"}) {
		t.Fatalf("Finish = %q", got)
	}
	f.Adopt(200)
	if got := describe(f.Finish(nil, adapterproto.TurnInterrupted)); !slices.Equal(got, []string{"ended person-200 interrupted"}) {
		t.Fatalf("Finish with no markers = %q", got)
	}
}

func TestEndAndAdopt(t *testing.T) {
	var f Follower
	f.Adopt(200)
	if ref, ok := f.Open(); !ok || ref != "person-200" {
		t.Fatalf("adopted turn: %q %v", ref, ok)
	}
	run(t, &f, []look{{StatusBusy, 200, 0, nil, []string{}}})
	if got := describe(f.End(adapterproto.TurnInterrupted)); !slices.Equal(got, []string{"ended person-200 interrupted"}) {
		t.Fatalf("End = %q", got)
	}
	if got := f.End(adapterproto.TurnInterrupted); got != nil {
		t.Fatalf("End with no turn = %v", got)
	}
	// The status that belonged to the ended turn does not open another.
	run(t, &f, []look{{StatusBusy, 200, 1, nil, []string{}}})
}
