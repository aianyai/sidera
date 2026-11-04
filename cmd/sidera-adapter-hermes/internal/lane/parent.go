package lane

import "time"

// Candidate is a card of a conversation as the board reports it.
type Candidate struct {
	ID     string
	Done   bool
	DoneAt time.Time
}

// ParentFor picks the card to make the parent of the request card of an inbound
// message that continues a conversation: the one finished most recently. A child
// of a parent that is done is ready at once, and Hermes copies the parent's
// subscription, with every routing field the command line cannot set, so the
// wake-up reaches the same chat and thread. A parent that is not done would leave
// the child waiting. It returns "" when there is none, and always for the shared
// orphan endpoint, whose cards come from unrelated places and would wake the
// wrong one; the caller then subscribes the fields recorded with the endpoint.
func ParentFor(endpointKey string, candidates []Candidate) string {
	if endpointKey == OrphanKey {
		return ""
	}
	best := -1
	for i, c := range candidates {
		if c.Done && (best < 0 || c.DoneAt.After(candidates[best].DoneAt)) {
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return candidates[best].ID
}
