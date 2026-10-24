package reply

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/aianyai/sidera/internal/delivery"
	"github.com/aianyai/sidera/internal/envelope"
	"github.com/aianyai/sidera/internal/ident"
	"github.com/aianyai/sidera/internal/record"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
)

// The property test drives Admit through random orders of replies, evidence and
// pulled reports and compares every decision with an oracle written separately
// here: a model that keeps the set of signed facts the sender has learned and
// answers "may this reply come back" from them alone, by the text of the rule.
// The oracle shares no code with the package: it does not use Arrival, Learn,
// windowStart, or the delivery state machine.

// A story is what really happened to one delivery at the target, as a timeline
// of states in seconds after t0. A report issued at time tau shows the state
// the story had reached by then, so reports are always consistent with each
// other, whatever the order in which the sender learns them.
type storyEvent struct {
	at    int
	state delivery.State
}

type story []storyEvent

// at returns the state reached by tau, and the time of the delivery if
// delivered by then.
func (s story) at(tau int) (state delivery.State, deliveredAt int, ok bool) {
	for _, e := range s {
		if e.at > tau {
			break
		}
		state, ok = e.state, true
		if e.state == delivery.StateDelivered {
			deliveredAt = e.at
		}
	}
	return
}

func randomStory(rng *rand.Rand, forControlRecipient bool) story {
	minutes := func(lo, hi int) int { return (lo + rng.Intn(hi-lo+1)) * 60 }
	a := minutes(0, 20*60)
	choices := 5
	if forControlRecipient {
		choices = 3
	}
	switch rng.Intn(choices) {
	case 0:
		return story{{a, delivery.StateAccepted}, {a + minutes(1, 240), delivery.StateDelivered}}
	case 1:
		return story{{a, delivery.StateAccepted}}
	case 2:
		return nil
	case 3:
		u := a + minutes(1, 120)
		return story{{a, delivery.StateAccepted}, {u, delivery.StateDeliveryUnknown}, {u + minutes(1, 120), delivery.StateDelivered}}
	default:
		return story{{a, delivery.StateRejected}}
	}
}

// fact is one signed report the sender has learned.
type fact struct {
	state       delivery.State
	reportedAt  int
	deliveredAt int
}

type oracle struct {
	enabled   bool
	bodyOK    bool // the control signal had an initial message
	isControl bool
	bound     bool // a verified control reply bound the session
	facts     map[string][]fact
}

func key(e ident.EndpointID) string { return string(e.Bytes()) }

func (o *oracle) tracked(e ident.EndpointID) bool {
	return e == runtimeR || (o.bound && e == sessionD)
}

func (o *oracle) learnReport(e ident.EndpointID, f fact) {
	if o.tracked(e) {
		o.facts[key(e)] = append(o.facts[key(e)], f)
	}
}

// start: the earliest delivery time of a delivered report; else the earliest
// issue time of a report that shows arrival; else none.
func (o *oracle) start(e ident.EndpointID) (int, string, bool) {
	best, bestArr := -1, -1
	for _, f := range o.facts[key(e)] {
		if f.state == delivery.StateDelivered && (best < 0 || f.deliveredAt < best) {
			best = f.deliveredAt
		}
		switch f.state {
		case delivery.StateAccepted, delivery.StateDelivered, delivery.StateDeliveryUnknown:
			if bestArr < 0 || f.reportedAt < bestArr {
				bestArr = f.reportedAt
			}
		}
	}
	switch {
	case best >= 0:
		return best, "delivered", true
	case bestArr >= 0:
		return bestArr, "arrival", true
	}
	return 0, "", false
}

type evidenceMode int

const (
	evNone evidenceMode = iota
	evReport
	evReply
	evBoth
	evForged // a report signed by a stranger's network key
)

func TestReplyPermissionAgainstAnOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(20261023))
	counts := map[string]int{}
	other := onA(8)
	stranger := onC(9)
	secs := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Second) }

	for trial := 0; trial < 600; trial++ {
		// the message: 40% plain, 50% session.create with a body, 10% without
		var outbound envelope.Outbound
		model := &oracle{facts: map[string][]fact{}}
		var outcome adapterproto.ControlCreateOutcome
		switch r := rng.Intn(10); {
		case r < 4:
			outbound = messageOutbound()
		case r < 9:
			outbound, model.isControl, model.bodyOK = createOutbound(true), true, true
		default:
			outbound, model.isControl = createOutbound(false), true
		}
		if model.isControl {
			outcome = []adapterproto.ControlCreateOutcome{adapterproto.CreateReplyCreated, adapterproto.CreateReplyUnconfirmed, adapterproto.CreateReplyFailed}[rng.Intn(3)]
		}
		storyR := randomStory(rng, model.isControl)
		var storyD story
		if outcome != adapterproto.CreateReplyFailed {
			storyD = randomStory(rng, false)
		}
		controlIssued := rng.Intn(20*60) * 1
		reply := func() record.Record {
			res := envelope.CreateResult{Outcome: outcome, SessionEndpoint: sessionD}
			switch outcome {
			case adapterproto.CreateReplyUnconfirmed:
				res.Reason = adapterproto.CreateReasonReadyTimeout
			case adapterproto.CreateReplyFailed:
				res.SessionEndpoint, res.Reason = ident.EndpointID{}, adapterproto.CreateReasonNativeError
			}
			return sealCreateReply(t, "m-1", res, secs(controlIssued), idA, netA)
		}
		binds := model.isControl && model.bodyOK && outcome != adapterproto.CreateReplyFailed
		now := 0
		for step := 0; step < 14; step++ {
			now += (1 + rng.Intn(360)) * 60 // up to 6 hours: the window is 24
			tau := now - rng.Intn(20)*60
			if tau < 0 {
				tau = 0
			}
			// a report the target could have issued at tau for endpoint e
			reportFor := func(e ident.EndpointID, s story) (record.Record, fact, bool) {
				st, delAt, ok := s.at(tau)
				if !ok {
					return record.Record{}, fact{}, false
				}
				rep := delivery.StatusReport{State: st}
				var del time.Time
				if st == delivery.StateRejected {
					rep.Reject = delivery.RejectBacklogExpired
				}
				if st == delivery.StateDelivered {
					del = secs(delAt)
				}
				return sealReport(t, "m-1", e, rep, secs(tau), del, idA, netA), fact{state: st, reportedAt: tau, deliveredAt: delAt}, true
			}
			switch kind := rng.Intn(10); {
			case kind < 3: // the sender pulls a status report
				e, s := ident.EndpointID(runtimeR), storyR
				if rng.Intn(2) == 0 {
					e, s = sessionD, storyD
				}
				rec, f, ok := reportFor(e, s)
				if !ok {
					continue
				}
				sr, err := envelope.VerifyStatusReport(rec, nodeOf(idA), nodeOf(idB), acceptedFor(idA, netA), secs(now))
				requireNoErr(t, err)
				n, eff, _, err := outbound.ApplyReport(secs(now), sr)
				requireNoErr(t, err)
				outbound = n
				if model.tracked(e) {
					model.learnReport(e, f)
					if eff != envelope.ReportApplied {
						t.Fatalf("a tracked report was not applied")
					}
					counts["pulled report"]++
				} else if eff != envelope.ReportNotTracked {
					t.Fatalf("an untracked report was applied")
				}
			case kind < 4 && model.isControl: // the sender pulls the control reply
				sc, err := envelope.VerifyControlReply(reply(), nodeOf(idA), nodeOf(idB), acceptedFor(idA, netA), secs(now))
				requireNoErr(t, err)
				if secs(controlIssued).After(secs(now).Add(2 * time.Minute)) {
					continue // the reply is dated after now: not issued yet
				}
				h, err := envelope.HandleControlReply(secs(now), outbound, sc, nil)
				requireNoErr(t, err)
				outbound = h.Outbound
				if binds && !model.bound {
					counts["bound by a pulled reply"]++
				}
				model.bound = model.bound || binds
			default: // a reply arrives
				replier := []ident.Address{onA(2), onA(2), onA(3), onA(3), other, stranger}[rng.Intn(6)]
				mode := evidenceMode(rng.Intn(5))
				if rng.Intn(3) == 0 {
					mode = evNone
				}
				enabled := rng.Intn(20) != 0
				wrongTarget := rng.Intn(40) == 0
				var evidence []record.Record
				var repFact fact
				haveReport := false
				var replyRec record.Record
				haveReply := false
				if (mode == evReply || mode == evBoth) && model.isControl && secs(controlIssued).Before(secs(now).Add(2*time.Minute)) {
					replyRec, haveReply = reply(), true
				}
				if mode == evReport || mode == evBoth {
					s := story(nil)
					switch replier.Endpoint {
					case runtimeR:
						s = storyR
					case sessionD:
						s = storyD
					}
					if rec, f, ok := reportFor(replier.Endpoint, s); ok {
						evidence, repFact, haveReport = append(evidence, rec), f, true
					} else if replier.Endpoint != runtimeR && replier.Endpoint != sessionD {
						// an endpoint with no story: a genuine report that it was accepted
						rec := acceptedReport(t, replier.Endpoint, secs(tau))
						evidence, haveReport = append(evidence, rec), true
						repFact = fact{state: delivery.StateAccepted, reportedAt: tau}
					}
				}
				if mode == evForged {
					evidence = append(evidence, sealReport(t, "m-1", replier.Endpoint, delivery.StatusReport{State: delivery.StateDelivered}, secs(tau), secs(tau), idC, netC))
				}
				if haveReply {
					evidence = append([]record.Record{replyRec}, evidence...)
				}
				if !haveReport && !haveReply && mode != evForged {
					evidence = nil
				}

				req := request(&outbound, replier, secs(now), evidence...)
				req.Enabled = enabled
				if wrongTarget {
					req.Target = ep(0x0b)
				}
				adm := mustAdmit(t, req)

				// the oracle, in the order of the rule
				var want Reason
				switch {
				case !enabled:
					want = ReasonDisabled
				case wrongTarget:
					want = ReasonNotSender
				case replier.Node != nodeOf(idA):
					want = ReasonNotRecipient
				default:
					if haveReply {
						model.bound = model.bound || binds
						if binds {
							counts["bound by evidence"]++
						}
					}
					if haveReport {
						model.learnReport(replier.Endpoint, repFact)
					}
					var s int
					var why string
					var ok bool
					switch {
					case !model.tracked(replier.Endpoint):
						want = ReasonNotRecipient
					case model.isControl && replier.Endpoint == runtimeR:
						// the result of a control signal returns as its control
						// reply: its original recipient never gets permission
						want = ReasonControlSignal
					default:
						if s, why, ok = model.start(replier.Endpoint); !ok {
							want = ReasonNoArrivalProof
						} else if now >= s+24*3600 {
							want = ReasonWindowClosed
						} else if why == "delivered" {
							want = ReasonDeliveredWindow
						} else {
							want = ReasonArrivalWindow
						}
					}
				}
				if read := enabled && !wrongTarget && replier.Node == nodeOf(idA); read && len(evidence) > 0 {
					// forged evidence is discarded and reported; genuine evidence never is
					if (mode == evForged) != (adm.EvidenceError != nil) {
						t.Fatalf("trial %d step %d: mode %d, evidence error %v", trial, step, mode, adm.EvidenceError)
					}
					if mode == evForged {
						counts["evidence discarded"]++
					}
				}
				if adm.Decision.Reason != want {
					t.Fatalf("trial %d step %d: replier %v mode %d now %ds: reason %v, oracle %v (evidence error: %v)",
						trial, step, replier.Endpoint, mode, now, adm.Decision.Reason, want, adm.EvidenceError)
				}
				counts[want.String()]++
				if want == ReasonDeliveredWindow || want == ReasonArrivalWindow {
					if replier.Endpoint == sessionD {
						counts["granted to a derived session"]++
					}
					s, _, _ := model.start(replier.Endpoint)
					if !adm.Decision.Until.Equal(secs(s + 24*3600)) {
						t.Fatalf("window end %v, oracle %v", adm.Decision.Until, secs(s+24*3600))
					}
				}
				if want != ReasonDisabled && want != ReasonNotSender && replier.Node == nodeOf(idA) {
					outbound = *adm.Outbound // commit
				}
			}
		}
	}
	for name, min := range map[string]int{
		"delivered_window": 100, "arrival_window": 150, "window_closed": 80, "no_arrival_proof": 150, "not_recipient": 300,
		"evidence discarded": 20, "control_signal": 60, "disabled": 20, "not_sender": 5,
		"bound by evidence": 100, "bound by a pulled reply": 50, "granted to a derived session": 80, "pulled report": 300,
	} {
		if counts[name] < min {
			t.Errorf("path %q taken %d times, want at least %d", name, counts[name], min)
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-32s %d", k, counts[k])
	}
}
