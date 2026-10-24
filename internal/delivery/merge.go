package delivery

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aianyai/sidera/internal/ident"
)

// Decided feedback values. Each has its reason with it, and
// DefaultFeedbackParams returns them together.
const (
	// DefaultFeedbackMergeWindow is how long a group of notices waits for more
	// of its kind. A burst of notices, such as 20 messages sent at once to an
	// unreachable target, arrives within seconds, so a minute merges it into
	// one. An unreachable notice is due UnreachableHintAfter after the first
	// failure, so it reaches the sender within 3 minutes, long before the
	// default delivery deadline of 10 minutes, while the sender can still send
	// elsewhere or give up.
	DefaultFeedbackMergeWindow = time.Minute
	// DefaultFeedbackRateCap is the most notices a sender endpoint receives per
	// rate window. A sender rarely meets more than 10 kinds of trouble in
	// 10 minutes, and even a burst of 30 only delays its last notice by 20
	// minutes. Each notice is a message that wakes the sender's session, so
	// cap and window together stay at the prompt cap of an endpoint per hour.
	DefaultFeedbackRateCap = 10
	// DefaultFeedbackRateWindow is the span over which the rate cap is counted.
	// It is 10 minutes: 10 notices per window is 60 an hour, and within one
	// queue time at most 30 notices wait in the sender's queue.
	DefaultFeedbackRateWindow = 10 * time.Minute
)

// FeedbackParams are the settings of feedback merging and rate limiting.
// Validate refuses zero values so an unset parameter cannot silently disable
// merging or the rate cap. DefaultFeedbackParams returns the decided values.
type FeedbackParams struct {
	// MergeWindow is how long a group of notices that share a merge key waits
	// for more of its kind before it is sent as one notice.
	MergeWindow time.Duration
	// RateCap is the most notices one sender endpoint receives per RateWindow.
	// Notices over the cap are held and sent in a later window, never dropped,
	// because a dropped anomaly would turn "no feedback means delivered" into a
	// lie.
	RateCap    int
	RateWindow time.Duration
}

// DefaultFeedbackParams returns the decided values.
func DefaultFeedbackParams() FeedbackParams {
	return FeedbackParams{
		MergeWindow: DefaultFeedbackMergeWindow,
		RateCap:     DefaultFeedbackRateCap,
		RateWindow:  DefaultFeedbackRateWindow,
	}
}

// Validate checks that every parameter is set.
func (p FeedbackParams) Validate() error {
	if p.MergeWindow <= 0 || p.RateCap <= 0 || p.RateWindow <= 0 {
		return fmt.Errorf("%w: feedback parameters must all be positive: %+v", ErrInvalidInput, p)
	}
	return nil
}

// mergeDueAt is the instant at which a group whose first item was decided at
// opened has waited out the merge window.
func mergeDueAt(opened time.Time, p FeedbackParams) time.Time {
	return opened.Add(p.MergeWindow)
}

// rateReleaseAt is the instant at which a notice sent at sentAt stops counting
// toward the rate cap: the end of its rate window. It is the one definition of
// that boundary; Flush and NextFlush both read it.
func rateReleaseAt(sentAt time.Time, p FeedbackParams) time.Time {
	return sentAt.Add(p.RateWindow)
}

// countsTowardCap reports whether a notice sent at sentAt still counts toward
// the rate cap at now.
func countsTowardCap(sentAt, now time.Time, p FeedbackParams) bool {
	return now.Before(rateReleaseAt(sentAt, p))
}

// DeliveryRef names one delivery for feedback purposes.
type DeliveryRef struct {
	// Sender is the sender endpoint that receives the feedback.
	Sender    ident.Address
	Message   MessageID
	Recipient ident.Address
}

// Item is one pending feedback decision about one delivery.
type Item struct {
	Ref      DeliveryRef
	Feedback Feedback
	// At is when the decision was made; the merge window counts from it.
	At time.Time
}

// MergeClass says what a group of notices has in common.
type MergeClass uint8

const (
	// MergeByMessage: anomalies of one message, so a multi-recipient message
	// is reported once however many of its recipients are affected.
	MergeByMessage MergeClass = iota + 1
	// MergeByTargetNode: unreachable hints of one sender endpoint for one
	// target node, so twenty messages to an offline node give one notice.
	MergeByTargetNode
	// MergeByTargetEndpoint: delivery_blocked reports for one target endpoint.
	MergeByTargetEndpoint
	// MergeRecoveryByTargetEndpoint: the recoveries of those blocks, kept apart
	// from the blocks so the recovery is a notice of its own.
	MergeRecoveryByTargetEndpoint
	// MergeExpiryByTargetNode: expired_unconfirmed notices of one sender
	// endpoint for one target node. The messages that went unreachable go
	// unconfirmed together when the node stays offline past their deadlines, so
	// they give one notice again, kept apart from the unreachable hints because
	// it is news of its own.
	MergeExpiryByTargetNode
)

// MergeKey is what notices must share to be sent as one. Fields that the class
// does not use are zero.
type MergeKey struct {
	Class   MergeClass
	Sender  ident.Address
	Message MessageID     // MergeByMessage
	Node    ident.NodeID  // MergeByTargetNode, MergeExpiryByTargetNode
	Target  ident.Address // MergeByTargetEndpoint, MergeRecoveryByTargetEndpoint
}

// MergeKeyOf returns the merge key of an item.
func MergeKeyOf(it Item) MergeKey {
	k := MergeKey{Sender: it.Ref.Sender}
	switch it.Feedback.Kind {
	case KindUnreachable:
		k.Class, k.Node = MergeByTargetNode, it.Ref.Recipient.Node
	case KindExpiredUnconfirmed:
		k.Class, k.Node = MergeExpiryByTargetNode, it.Ref.Recipient.Node
	case KindDeliveryBlocked:
		k.Class, k.Target = MergeByTargetEndpoint, it.Ref.Recipient
	case KindBlockedRecovered:
		k.Class, k.Target = MergeRecoveryByTargetEndpoint, it.Ref.Recipient
	default:
		k.Class, k.Message = MergeByMessage, it.Ref.Message
	}
	return k
}

func (k MergeKey) sortKey() string {
	return fmt.Sprintf("%d/%s/%s/%s/%s", k.Class, k.Sender, k.Node, k.Target, k.Message)
}

// Notice is one merged notice for a sender endpoint.
type Notice struct {
	Key    MergeKey
	Sender ident.Address
	Items  []Item
}

// SentNotice remembers when a notice went to a sender endpoint, for the cap.
type SentNotice struct {
	Sender ident.Address
	At     time.Time
}

// FeedbackQueue holds the feedback of a sender's node that has not been sent
// yet, and the notices sent recently. Its fields are exported so the caller can
// persist it in the transaction that records the decisions.
type FeedbackQueue struct {
	Pending []Item
	Sent    []SentNotice
}

// ErrInvalidItem reports an item that cannot be queued.
var ErrInvalidItem = errors.New("invalid feedback item")

// Add queues an item. An item for a (delivery, kind) pair that is already
// queued is ignored, so replaying the decision cannot send a notice twice.
func (q *FeedbackQueue) Add(it Item) error {
	if !it.Feedback.Kind.valid() || it.Ref.Sender.IsZero() || it.Ref.Recipient.IsZero() || it.Ref.Message == "" {
		return fmt.Errorf("%w: %+v", ErrInvalidItem, it)
	}
	for _, p := range q.Pending {
		if p.Ref == it.Ref && p.Feedback.Kind == it.Feedback.Kind {
			return nil
		}
	}
	q.Pending = append(q.Pending, it)
	return nil
}

// Flush returns the notices due at now and removes their items from the queue.
// A group is due once MergeWindow has passed since its first item was decided,
// unless the sender endpoint already received RateCap notices within the last
// RateWindow, in which case the group stays queued. Groups are tried oldest
// first, so held notices go out in the order they were decided.
func (q *FeedbackQueue) Flush(now time.Time, p FeedbackParams) ([]Notice, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	recent := q.Sent[:0]
	perSender := map[ident.Address]int{}
	for _, s := range q.Sent {
		if countsTowardCap(s.At, now, p) {
			recent = append(recent, s)
			perSender[s.Sender]++
		}
	}
	q.Sent = recent

	groups := map[MergeKey]*Notice{}
	opened := map[MergeKey]time.Time{}
	for _, it := range q.Pending {
		k := MergeKeyOf(it)
		g, ok := groups[k]
		if !ok {
			g = &Notice{Key: k, Sender: it.Ref.Sender}
			groups[k] = g
			opened[k] = it.At
		}
		g.Items = append(g.Items, it)
		if it.At.Before(opened[k]) {
			opened[k] = it.At
		}
	}
	keys := make([]MergeKey, 0, len(groups))
	for k := range groups {
		if !now.Before(mergeDueAt(opened[k], p)) {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if !opened[keys[i]].Equal(opened[keys[j]]) {
			return opened[keys[i]].Before(opened[keys[j]])
		}
		return keys[i].sortKey() < keys[j].sortKey()
	})

	var notices []Notice
	sent := map[MergeKey]bool{}
	for _, k := range keys {
		g := groups[k]
		if perSender[g.Sender] >= p.RateCap {
			continue
		}
		perSender[g.Sender]++
		q.Sent = append(q.Sent, SentNotice{Sender: g.Sender, At: now})
		sort.SliceStable(g.Items, func(i, j int) bool { return g.Items[i].At.Before(g.Items[j].At) })
		notices = append(notices, *g)
		sent[k] = true
	}
	rest := q.Pending[:0]
	for _, it := range q.Pending {
		if !sent[MergeKeyOf(it)] {
			rest = append(rest, it)
		}
	}
	q.Pending = rest
	return notices, nil
}
