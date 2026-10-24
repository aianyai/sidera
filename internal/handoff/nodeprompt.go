package handoff

import (
	"errors"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// ErrInvalidNodeInput is wrapped by errors about the input of DecideNodePrompts.
var ErrInvalidNodeInput = errors.New("handoff: invalid node prompt input")

// WaitingPrompt is one endpoint that waits to be prompted.
type WaitingPrompt struct {
	Endpoint     ident.EndpointID
	WaitingSince time.Time
	Input        PromptInput
}

// NodePromptDecision is the verdict for one waiting endpoint.
type NodePromptDecision struct {
	Endpoint ident.EndpointID
	Decision PromptDecision
}

// DecideNodePrompts is a stub.
func DecideNodePrompts(waiting []WaitingPrompt, running int, p PromptParams, now time.Time) ([]NodePromptDecision, error) {
	return nil, nil
}
