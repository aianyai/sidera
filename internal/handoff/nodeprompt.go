package handoff

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// ErrInvalidNodeInput is wrapped by errors about the input of DecideNodePrompts
// that is not a usable description of a node: a negative count, or an endpoint
// that is unset or listed twice.
var ErrInvalidNodeInput = errors.New("handoff: invalid node prompt input")

// WaitingPrompt is one endpoint that waits to be prompted.
type WaitingPrompt struct {
	Endpoint ident.EndpointID
	// WaitingSince is when the oldest message waiting for the endpoint arrived:
	// the same value as BlockedInput.OldestPendingAt, so a suspended message is
	// not counted. The messages of the endpoint that waited longest are the
	// closest to expiring, which is why this orders the release. Zero is not
	// accepted: it would sort before every real instant and so outrank every
	// other waiter.
	WaitingSince time.Time
	// Input is what DecidePrompt reads about the endpoint. Trigger is the
	// endpoint's pending trigger; see DecideNodePrompts.
	Input PromptInput
}

// NodePromptDecision is the verdict for one waiting endpoint.
type NodePromptDecision struct {
	Endpoint ident.EndpointID
	Decision PromptDecision
}

// DecideNodePrompts decides which of the waiting endpoints of a node are
// prompted now, so that the turns that prompts have started never exceed
// PromptParams.MaxConcurrentTurns.
//
// waiting holds every endpoint with a message to wake it for whose adapter is
// online. An endpoint whose adapter is offline is left out: it would take a slot
// for an attempt that is bound to fail, and that failure would then feed
// AssessBlocked.
//
// running is the number of turns that prompts started and that are still
// running on the node. A prompt counts from its dispatch, the moment the
// endpoint's Busy becomes true, until its turn ends or it is confirmed not to
// have started one. Only turns that prompts started count, and only those of
// online adapters: the state of an offline adapter's turn is unknown, and an
// unobservable turn must not stop the whole node. When the adapter returns and
// reports the turn still running, it is counted again, and running may then
// exceed the cap. No endpoint is released until it falls below the cap.
//
// Each endpoint is first judged by DecidePrompt, on its own. Only an endpoint
// that DecidePrompt would prompt takes a slot; any other keeps its own hold and
// reason and leaves the slot to the next waiter. A PromptSend of DecidePrompt
// alone never authorizes a prompt: the caller sends only to the endpoints this
// function releases. Of the endpoints that would be prompted, the ones that have
// waited longest are released, no more than the free slots (the cap less
// running); ties in WaitingSince go to the endpoint with the lower ID, in byte
// order, so the result does not depend on the order of the input. The others
// are held with PromptNodeFull.
//
// The result has one decision per waiting endpoint, ordered by WaitingSince and
// then by endpoint ID. The input is not modified.
//
// A hold with PromptNodeFull is not an attempt: the caller records nothing for
// it and it does not count toward the hourly cap. It is no failure either, so
// by itself it never makes delivery blocked; but an endpoint can wait long
// under the node cap, so when its adapter goes offline the caller must give the
// instant it did as Signals.Since, or the time held back would be counted as
// failure time.
//
// Nothing of the endpoint's own brings it back from a hold with PromptNodeFull:
// a freed slot is not an event of the waiting endpoint, and RetryAt covers only
// the hourly cap and the minimum interval. The principle is that whenever free
// slots may have grown, that is, when running falls or MaxConcurrentTurns rises,
// the caller calls DecideNodePrompts again for all waiting endpoints. That
// happens when
//
//   - a turn that a prompt started ends, in any way;
//   - a dispatched prompt is confirmed not to have started a turn;
//   - an adapter whose turns were counted goes offline;
//   - the cap is raised.
//
// Each endpoint is passed with the Trigger it has pending. A later arrival for
// an endpoint that is held does not replace TriggerTurnEnd; the trigger is kept
// until a prompt is sent to the endpoint.
func DecideNodePrompts(waiting []WaitingPrompt, running int, p PromptParams, now time.Time) ([]NodePromptDecision, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if running < 0 {
		return nil, fmt.Errorf("%w: running turns must not be negative, got %d", ErrInvalidNodeInput, running)
	}
	seen := make(map[ident.EndpointID]bool, len(waiting))
	for _, w := range waiting {
		switch {
		case w.Endpoint.IsZero():
			return nil, fmt.Errorf("%w: a waiting endpoint is unset", ErrInvalidNodeInput)
		case w.WaitingSince.IsZero():
			return nil, fmt.Errorf("%w: endpoint %v has no wait start", ErrInvalidNodeInput, w.Endpoint)
		case seen[w.Endpoint]:
			return nil, fmt.Errorf("%w: endpoint %v is listed twice", ErrInvalidNodeInput, w.Endpoint)
		}
		seen[w.Endpoint] = true
	}

	ordered := slices.Clone(waiting)
	slices.SortFunc(ordered, func(a, b WaitingPrompt) int {
		return cmp.Or(
			a.WaitingSince.Compare(b.WaitingSince),
			bytes.Compare(a.Endpoint.Bytes(), b.Endpoint.Bytes()),
		)
	})

	free := max(0, p.MaxConcurrentTurns-running)
	out := make([]NodePromptDecision, 0, len(ordered))
	for _, w := range ordered {
		d, err := DecidePrompt(w.Input, p, now)
		if err != nil {
			return nil, err
		}
		if d.Action == PromptSend {
			if free > 0 {
				free--
			} else {
				d = hold(PromptNodeFull)
			}
		}
		out = append(out, NodePromptDecision{Endpoint: w.Endpoint, Decision: d})
	}
	return out, nil
}
