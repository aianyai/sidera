package handoff

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// ep is the endpoint whose ID starts with the byte n. Endpoint IDs compare in
// byte order, so n is the rank of the ID.
func ep(n byte) ident.EndpointID {
	b := make([]byte, ident.EndpointIDLen)
	b[0] = n
	id, err := ident.EndpointIDFromBytes(b)
	if err != nil {
		panic(err)
	}
	return id
}

// arrival is the input of an endpoint with one message waiting and no earlier
// prompt: DecidePrompt sends it a first prompt.
func arrival(since time.Duration) PromptInput {
	return PromptInput{Trigger: TriggerArrival, Pending: 1, LastArrivalAt: after(since)}
}

func waiter(n byte, since time.Duration) WaitingPrompt {
	return WaitingPrompt{Endpoint: ep(n), WaitingSince: after(since), Input: arrival(since)}
}

func busyWaiter(n byte, since time.Duration) WaitingPrompt {
	w := waiter(n, since)
	w.Input.Busy = true
	return w
}

func emptyWaiter(n byte, since time.Duration) WaitingPrompt {
	w := waiter(n, since)
	w.Input.Pending = 0
	return w
}

// cappedWaiter has used its hourly allowance with prompts of unknown outcome.
func cappedWaiter(n byte, since time.Duration) WaitingPrompt {
	w := waiter(n, since)
	for i := 6; i >= 1; i-- {
		w.Input.Attempts = append(w.Input.Attempts, Attempt{At: after(nodeNow - time.Duration(i)*5*time.Minute), Outcome: AttemptUnknown, Kind: PromptKindFirst})
	}
	return w
}

// repromptWaiter left its delivered prompt unread and its turn has ended.
func repromptWaiter(n byte, since time.Duration) WaitingPrompt {
	w := waiter(n, since)
	w.Input.Trigger = TriggerTurnEnd
	w.Input.LastArrivalAt = after(since)
	w.Input.Attempts = []Attempt{delivered(since+time.Minute, PromptKindFirst)}
	return w
}

// nodeNow is the offset of the evaluation instant used by the tables.
const nodeNow = 3 * time.Hour

type nodeWant struct {
	endpoint byte
	action   PromptAction
	reason   PromptReason
}

func wantSend(n byte, r PromptReason) nodeWant { return nodeWant{n, PromptSend, r} }
func wantHold(n byte, r PromptReason) nodeWant { return nodeWant{n, PromptHold, r} }

// checkNodeDecisions asserts the shape of the output (one entry per waiting
// endpoint, in the stated order), and that each entry is either exactly what
// DecidePrompt says about the endpoint, or a bare node-full hold for an
// endpoint DecidePrompt would have prompted.
func checkNodeDecisions(t *testing.T, waiting []WaitingPrompt, got []NodePromptDecision, want []nodeWant, p PromptParams) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d decisions, want %d: %+v", len(got), len(want), got)
	}
	byEndpoint := map[ident.EndpointID]WaitingPrompt{}
	for _, w := range waiting {
		byEndpoint[w.Endpoint] = w
	}
	for i, w := range want {
		g := got[i]
		if g.Endpoint != ep(w.endpoint) {
			t.Fatalf("decision %d is for %v, want %v", i, g.Endpoint, ep(w.endpoint))
		}
		if g.Decision.Action != w.action || g.Decision.Reason != w.reason {
			t.Fatalf("decision %d (endpoint %d) = action %d reason %d, want action %d reason %d",
				i, w.endpoint, g.Decision.Action, g.Decision.Reason, w.action, w.reason)
		}
		own, err := DecidePrompt(byEndpoint[g.Endpoint].Input, p, after(nodeNow))
		if err != nil {
			t.Fatal(err)
		}
		if w.reason == PromptNodeFull {
			if own.Action != PromptSend {
				t.Fatalf("endpoint %d is held by the node cap but DecidePrompt says %+v", w.endpoint, own)
			}
			if g.Decision != (PromptDecision{Action: PromptHold, Reason: PromptNodeFull}) {
				t.Fatalf("a node-full hold carries more than action and reason: %+v", g.Decision)
			}
		} else if g.Decision != own {
			t.Fatalf("endpoint %d: got %+v, DecidePrompt says %+v", w.endpoint, g.Decision, own)
		}
	}
}

func TestDecideNodePrompts(t *testing.T) {
	rows := []struct {
		rule    string
		waiting []WaitingPrompt
		running int
		want    []nodeWant
	}{
		{"release up to the free slots, the rest are held by the node cap",
			[]WaitingPrompt{waiter(1, time.Minute), waiter(2, 2*time.Minute), waiter(3, 3*time.Minute), waiter(4, 4*time.Minute)}, 1,
			[]nodeWant{wantSend(1, PromptFirst), wantSend(2, PromptFirst), wantHold(3, PromptNodeFull), wantHold(4, PromptNodeFull)}},
		{"fewer waiting endpoints than free slots: all are released",
			[]WaitingPrompt{waiter(1, time.Minute), waiter(2, 2*time.Minute)}, 0,
			[]nodeWant{wantSend(1, PromptFirst), wantSend(2, PromptFirst)}},
		{"as many waiting endpoints as free slots: all are released",
			[]WaitingPrompt{waiter(1, time.Minute), waiter(2, 2*time.Minute)}, 1,
			[]nodeWant{wantSend(1, PromptFirst), wantSend(2, PromptFirst)}},
		{"earliest waiter first, whatever the order of the input",
			[]WaitingPrompt{waiter(3, 3*time.Minute), waiter(1, time.Minute), waiter(2, 2*time.Minute)}, 1,
			[]nodeWant{wantSend(1, PromptFirst), wantSend(2, PromptFirst), wantHold(3, PromptNodeFull)}},
		{"equal wait starts: the lower endpoint ID goes first",
			[]WaitingPrompt{waiter(3, time.Minute), waiter(1, time.Minute), waiter(2, time.Minute)}, 1,
			[]nodeWant{wantSend(1, PromptFirst), wantSend(2, PromptFirst), wantHold(3, PromptNodeFull)}},
		{"equal wait starts, input in the opposite order: same outcome",
			[]WaitingPrompt{waiter(1, time.Minute), waiter(2, time.Minute), waiter(3, time.Minute)}, 1,
			[]nodeWant{wantSend(1, PromptFirst), wantSend(2, PromptFirst), wantHold(3, PromptNodeFull)}},
		{"a later start outranks a lower endpoint ID: the start decides first",
			[]WaitingPrompt{waiter(1, 2*time.Minute), waiter(9, time.Minute)}, 2,
			[]nodeWant{wantSend(9, PromptFirst), wantHold(1, PromptNodeFull)}},
		{"zero free slots release none",
			[]WaitingPrompt{waiter(1, time.Minute), waiter(2, 2*time.Minute)}, 3,
			[]nodeWant{wantHold(1, PromptNodeFull), wantHold(2, PromptNodeFull)}},
		{"occupancy above the cap, as when an adapter comes back with turns still running, releases none",
			[]WaitingPrompt{waiter(1, time.Minute)}, 5,
			[]nodeWant{wantHold(1, PromptNodeFull)}},
		{"an endpoint whose session is busy is not released and holds no slot",
			[]WaitingPrompt{busyWaiter(1, time.Minute), waiter(2, 2*time.Minute), waiter(3, 3*time.Minute)}, 2,
			[]nodeWant{wantHold(1, PromptSessionBusy), wantSend(2, PromptFirst), wantHold(3, PromptNodeFull)}},
		{"an endpoint with nothing pending is not released and holds no slot",
			[]WaitingPrompt{emptyWaiter(1, time.Minute), waiter(2, 2*time.Minute)}, 2,
			[]nodeWant{wantHold(1, PromptNothingPending), wantSend(2, PromptFirst)}},
		{"an endpoint at its hourly cap keeps its own reason and holds no slot",
			[]WaitingPrompt{cappedWaiter(1, time.Minute), waiter(2, 2*time.Minute)}, 2,
			[]nodeWant{wantHold(1, PromptHourlyCap), wantSend(2, PromptFirst)}},
		{"an endpoint whose re-prompt is not due is not released and holds no slot",
			[]WaitingPrompt{unreadWaiter(1, time.Minute), waiter(2, 2*time.Minute)}, 2,
			[]nodeWant{wantHold(1, PromptUnreadWaiting), wantSend(2, PromptFirst)}},
		{"a re-prompt after a turn end competes for a slot like a first prompt",
			[]WaitingPrompt{repromptWaiter(1, time.Minute), waiter(2, 2*time.Minute)}, 2,
			[]nodeWant{wantSend(1, PromptReprompt), wantHold(2, PromptNodeFull)}},
		{"no waiting endpoints: nothing is decided", nil, 0, nil},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got, err := DecideNodePrompts(r.waiting, r.running, testPromptParams, after(nodeNow))
			if err != nil {
				t.Fatal(err)
			}
			checkNodeDecisions(t, r.waiting, got, r.want, testPromptParams)
		})
	}
}

// unreadWaiter has an unread delivered prompt and no new message since: its
// arrival does not wake it again.
func unreadWaiter(n byte, since time.Duration) WaitingPrompt {
	w := waiter(n, since)
	w.Input.LastArrivalAt = after(since - time.Minute)
	w.Input.Attempts = []Attempt{delivered(since, PromptKindFirst)}
	return w
}

func TestDecideNodePromptsRejectsInvalidInput(t *testing.T) {
	noCap := testPromptParams
	noCap.MaxConcurrentTurns = 0
	dup := []WaitingPrompt{waiter(1, time.Minute), waiter(1, 2*time.Minute)}
	noStart := waiter(1, time.Minute)
	noStart.WaitingSince = time.Time{}
	noID := waiter(1, time.Minute)
	noID.Endpoint = ident.EndpointID{}

	rows := []struct {
		rule    string
		waiting []WaitingPrompt
		running int
		p       PromptParams
		is      error
	}{
		{"an unset node cap is refused, never read as unlimited", []WaitingPrompt{waiter(1, time.Minute)}, 0, noCap, ErrInvalidParams},
		{"a negative running count is refused", []WaitingPrompt{waiter(1, time.Minute)}, -1, testPromptParams, ErrInvalidNodeInput},
		{"an endpoint listed twice is refused: it would be released twice", dup, 0, testPromptParams, ErrInvalidNodeInput},
		{"an unset wait start is refused: it would outrank every real one", []WaitingPrompt{noStart}, 0, testPromptParams, ErrInvalidNodeInput},
		{"an unset endpoint is refused", []WaitingPrompt{noID}, 0, testPromptParams, ErrInvalidNodeInput},
	}
	for _, r := range rows {
		t.Run(r.rule, func(t *testing.T) {
			got, err := DecideNodePrompts(r.waiting, r.running, r.p, after(nodeNow))
			if !errors.Is(err, r.is) {
				t.Fatalf("err = %v, want one wrapping %v", err, r.is)
			}
			if got != nil {
				t.Fatalf("decisions returned with an error: %+v", got)
			}
		})
	}
}

func TestDecideNodePromptsDoesNotModifyItsInput(t *testing.T) {
	waiting := []WaitingPrompt{cappedWaiter(3, 3*time.Minute), repromptWaiter(1, time.Minute), waiter(2, 2*time.Minute), waiter(4, time.Minute)}
	want := make([]WaitingPrompt, len(waiting))
	for i, w := range waiting {
		w.Input.Attempts = append([]Attempt(nil), w.Input.Attempts...)
		want[i] = w
	}
	if _, err := DecideNodePrompts(waiting, 1, testPromptParams, after(nodeNow)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(waiting, want) {
		t.Fatalf("input changed:\n got %+v\nwant %+v", waiting, want)
	}
}

// A freed slot is not an event of the waiting endpoints. The caller evaluates
// all of them again whenever a prompt-started turn ends, and the earliest
// waiter gets the slot.
func TestNodePromptScenarioFreedSlotReleasesWaitersOldestFirst(t *testing.T) {
	p := testPromptParams
	p.MaxConcurrentTurns = 2
	waiting := []WaitingPrompt{waiter(5, 3*time.Minute), waiter(7, time.Minute), waiter(6, 2*time.Minute)}
	running := 2
	now := after(10 * time.Minute)

	decide := func() []NodePromptDecision {
		t.Helper()
		got, err := DecideNodePrompts(waiting, running, p, now)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	releasedOf := func(got []NodePromptDecision) []ident.EndpointID {
		var ids []ident.EndpointID
		for _, g := range got {
			if g.Decision.Action == PromptSend {
				ids = append(ids, g.Endpoint)
			}
		}
		return ids
	}
	drop := func(id ident.EndpointID) {
		for i, w := range waiting {
			if w.Endpoint == id {
				waiting = append(waiting[:i], waiting[i+1:]...)
				return
			}
		}
		t.Fatalf("endpoint %v is not waiting", id)
	}

	if got := releasedOf(decide()); len(got) != 0 {
		t.Fatalf("the node is full, but %v were released", got)
	}

	// One turn ends: the endpoint that waited longest is released.
	running = 1
	now = now.Add(time.Minute)
	got := releasedOf(decide())
	if !reflect.DeepEqual(got, []ident.EndpointID{ep(7)}) {
		t.Fatalf("released %v, want only the earliest waiter %v", got, ep(7))
	}
	drop(ep(7)) // its prompt was delivered: it is busy and counted as running
	running = 2

	// Nothing frees up: the next waiters are still held.
	if got := releasedOf(decide()); len(got) != 0 {
		t.Fatalf("released %v with the node full", got)
	}

	// Two turns end at once: both remaining waiters go, in order.
	running = 0
	got = releasedOf(decide())
	if !reflect.DeepEqual(got, []ident.EndpointID{ep(6), ep(5)}) {
		t.Fatalf("released %v, want %v then %v", got, ep(6), ep(5))
	}
}

// Being held back is not an attempt: nothing is recorded for it, so the
// waiting endpoint is neither woken nor blamed for it, and delivery to it is
// not blocked however long the wait lasts.
func TestNodeCapHoldIsNeitherAnAttemptNorBlocked(t *testing.T) {
	p := testPromptParams
	p.MaxConcurrentTurns = 1
	w := waiter(1, 0)
	now := after(40 * time.Minute)

	got, err := DecideNodePrompts([]WaitingPrompt{w}, 1, p, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Decision != (PromptDecision{Action: PromptHold, Reason: PromptNodeFull}) {
		t.Fatalf("got %+v, want a node-full hold", got)
	}

	// The caller records nothing for the hold, so the blocked rule sees an
	// endpoint with no attempts and no active cause.
	v := AssessBlocked(BlockedInput{OldestPendingAt: w.WaitingSince}, DefaultBlockedParams(), now)
	if v.Status != NotBlocked || v.Reason != ReasonNoFailures {
		t.Fatalf("held back for 40 minutes: verdict %+v, want not blocked", v)
	}

	// When the slot frees, the prompt is the first one: the hold left no mark
	// on the hourly allowance or on the re-prompt chain.
	got, err = DecideNodePrompts([]WaitingPrompt{w}, 0, p, now)
	if err != nil {
		t.Fatal(err)
	}
	want := PromptDecision{Action: PromptSend, Reason: PromptFirst, Kind: PromptKindFirst, Count: 1}
	if len(got) != 1 || got[0].Decision != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// nodeOracle is written apart from DecideNodePrompts: it keeps the endpoints
// in an insertion-sorted list, compares IDs as hex text, and picks the
// released endpoints one at a time. Whether one endpoint may be prompted at all
// is DecidePrompt's rule, which these tests do not repeat.
func nodeOracle(t *testing.T, waiting []WaitingPrompt, running int, p PromptParams, now time.Time) []NodePromptDecision {
	t.Helper()
	less := func(a, b WaitingPrompt) bool {
		if !a.WaitingSince.Equal(b.WaitingSince) {
			return a.WaitingSince.Before(b.WaitingSince)
		}
		return fmt.Sprintf("%x", a.Endpoint.Bytes()) < fmt.Sprintf("%x", b.Endpoint.Bytes())
	}
	var sorted []WaitingPrompt
	for _, w := range waiting {
		i := len(sorted)
		sorted = append(sorted, w)
		for i > 0 && less(sorted[i], sorted[i-1]) {
			sorted[i], sorted[i-1] = sorted[i-1], sorted[i]
			i--
		}
	}
	free := p.MaxConcurrentTurns - running
	out := make([]NodePromptDecision, 0, len(sorted))
	for _, w := range sorted {
		d, err := DecidePrompt(w.Input, p, now)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action == PromptSend {
			if free > 0 {
				free--
			} else {
				d = PromptDecision{Action: PromptHold, Reason: PromptNodeFull}
			}
		}
		out = append(out, NodePromptDecision{Endpoint: w.Endpoint, Decision: d})
	}
	return out
}

func TestDecideNodePromptsProperties(t *testing.T) {
	const iterations = 4000
	rng := rand.New(rand.NewSource(43))
	now := after(nodeNow)
	counts := map[string]int{}

	for iter := 0; iter < iterations; iter++ {
		p := PromptParams{MaxPerHour: 6, MinInterval: 5 * time.Minute, MaxConcurrentTurns: 1 + rng.Intn(8)}
		running := rng.Intn(12)
		n := rng.Intn(13)
		ids := rng.Perm(12)
		var waiting []WaitingPrompt
		for i := 0; i < n; i++ {
			// Few distinct minutes, so that equal wait starts are common.
			since := time.Duration(rng.Intn(6)) * time.Minute
			var w WaitingPrompt
			switch rng.Intn(6) {
			case 0, 1:
				w = waiter(byte(ids[i]+1), since)
				if rng.Intn(2) == 0 {
					w.Input.Trigger = TriggerTurnEnd
				}
				w.Input.Pending = 1 + rng.Intn(5)
			case 2:
				w = busyWaiter(byte(ids[i]+1), since)
			case 3:
				w = emptyWaiter(byte(ids[i]+1), since)
			case 4:
				w = cappedWaiter(byte(ids[i]+1), since)
			case 5:
				if rng.Intn(2) == 0 {
					w = unreadWaiter(byte(ids[i]+1), since)
				} else {
					w = repromptWaiter(byte(ids[i]+1), since)
				}
			}
			waiting = append(waiting, w)
		}

		got, err := DecideNodePrompts(waiting, running, p, now)
		if err != nil {
			t.Fatal(err)
		}
		want := nodeOracle(t, waiting, running, p, now)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d (cap %d, running %d):\n got %+v\nwant %+v\nwaiting %+v", iter, p.MaxConcurrentTurns, running, got, want, waiting)
		}

		// Coverage of the paths the oracle was supposed to exercise.
		free := p.MaxConcurrentTurns - running
		candidates, released := 0, 0
		for _, g := range got {
			counts["reason "+fmt.Sprint(g.Decision.Reason)]++
			if g.Decision.Action == PromptSend {
				released++
			}
			if g.Decision.Action == PromptSend || g.Decision.Reason == PromptNodeFull {
				candidates++
			}
		}
		switch {
		case candidates == 0:
			counts["no candidates"]++
		case free <= 0:
			counts["candidates, no free slot"]++
		case candidates > free:
			counts["candidates exceed free slots"]++
		case candidates < free:
			counts["free slots exceed candidates"]++
		default:
			counts["candidates equal free slots"]++
		}
		if running > p.MaxConcurrentTurns {
			counts["running above cap"]++
		}
		if released < len(got) && released > 0 {
			counts["a non-released endpoint among released ones"]++
		}
		// A tie of wait starts across the boundary between released and held.
		var last, first *WaitingPrompt
		for i := range got {
			for j := range waiting {
				if waiting[j].Endpoint != got[i].Endpoint {
					continue
				}
				if got[i].Decision.Action == PromptSend {
					last = &waiting[j]
				} else if got[i].Decision.Reason == PromptNodeFull && first == nil {
					first = &waiting[j]
				}
			}
		}
		if last != nil && first != nil && last.WaitingSince.Equal(first.WaitingSince) {
			counts["tie across the release boundary"]++
		}
	}

	t.Logf("coverage: %v", counts)
	for _, key := range []string{
		"no candidates", "candidates, no free slot", "candidates exceed free slots",
		"free slots exceed candidates", "candidates equal free slots", "running above cap",
		"a non-released endpoint among released ones", "tie across the release boundary",
		"reason " + fmt.Sprint(PromptFirst), "reason " + fmt.Sprint(PromptReprompt),
		"reason " + fmt.Sprint(PromptSessionBusy), "reason " + fmt.Sprint(PromptNothingPending),
		"reason " + fmt.Sprint(PromptHourlyCap), "reason " + fmt.Sprint(PromptUnreadWaiting),
		"reason " + fmt.Sprint(PromptNodeFull),
	} {
		if counts[key] < 100 {
			t.Errorf("path %q taken %d times, want at least 100", key, counts[key])
		}
	}
}
