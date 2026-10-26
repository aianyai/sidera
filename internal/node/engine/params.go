package engine

import (
	"time"

	"github.com/aianyai/sidera/internal/delivery"
)

// Params are the tunables of the loop and its batches. A zero field takes the
// default in DefaultParams: nothing here is security relevant, and zero never
// means "no limit".
type Params struct {
	// InboxLen is how many commands wait for the loop. When it is full, an
	// external submitter is refused after SubmitWait; an internal one (a worker,
	// the adapter host's forwarder of notifications) waits, because its order
	// must be kept and its back pressure is the buffer behind it.
	InboxLen int
	// SubmitWait is how long an external submitter waits for room in the inbox,
	// on the real clock, before it gets ReasonLoopBusy.
	SubmitWait time.Duration
	// ChunkReplaces is the most records one transaction of a long batch
	// replaces, and so the most the loop is held for by one chunk: closing the
	// queued deliveries of a closed endpoint is the case that needs it. The
	// target records of a prompt or a block are the known exception: they go in
	// one call, because the head is written last and every call writes it, so
	// their bound is the endpoint's backlog limit plus the system feedback.
	ChunkReplaces int
	// ChunkRemoves is the most files one transaction of a sweep removes.
	ChunkRemoves int
	// ChunkTickRecords is the most records one command runs Tick on.
	ChunkTickRecords int
	// RetryBackoff is how long internal work that failed with an error a retry
	// may cure waits before it is tried again, on the injected clock.
	RetryBackoff time.Duration
	// ResendBackoff is how long a sender waits after a send that got no receipt
	// before it sends the same bytes again, on the injected clock. A message is
	// sent again until its delivery deadline; the target deduplicates by message
	// id, so a send that was processed and whose receipt was lost is harmless.
	ResendBackoff time.Duration
	// PullInterval is how long a sender waits between two pulls of the reports of
	// a delivery that is not final, until its report deadline.
	PullInterval time.Duration
	// Feedback are the merge window and the rate cap of the notices the node tells
	// its endpoints; delivery.FeedbackParams says what each is for.
	Feedback delivery.FeedbackParams
}

// DefaultParams returns the defaults. The chunk sizes keep the longest
// uninterrupted write of the loop to well under a second on a disk that takes
// 4 to 8 ms per directory sync: 16 replaced files each synced, 256 removals
// and one directory sync, 512 records computed in memory.
func DefaultParams() Params {
	return Params{
		InboxLen:         1024,
		SubmitWait:       10 * time.Second,
		ChunkReplaces:    16,
		ChunkRemoves:     256,
		ChunkTickRecords: 512,
		RetryBackoff:     time.Minute,
		ResendBackoff:    30 * time.Second,
		PullInterval:     time.Minute,
		Feedback:         delivery.DefaultFeedbackParams(),
	}
}

func (p Params) withDefaults() Params {
	d := DefaultParams()
	if p.InboxLen <= 0 {
		p.InboxLen = d.InboxLen
	}
	if p.SubmitWait <= 0 {
		p.SubmitWait = d.SubmitWait
	}
	if p.ChunkReplaces <= 0 {
		p.ChunkReplaces = d.ChunkReplaces
	}
	if p.ChunkRemoves <= 0 {
		p.ChunkRemoves = d.ChunkRemoves
	}
	if p.ChunkTickRecords <= 0 {
		p.ChunkTickRecords = d.ChunkTickRecords
	}
	if p.RetryBackoff <= 0 {
		p.RetryBackoff = d.RetryBackoff
	}
	if p.ResendBackoff <= 0 {
		p.ResendBackoff = d.ResendBackoff
	}
	if p.PullInterval <= 0 {
		p.PullInterval = d.PullInterval
	}
	if p.Feedback.MergeWindow <= 0 {
		p.Feedback.MergeWindow = d.Feedback.MergeWindow
	}
	if p.Feedback.RateCap <= 0 {
		p.Feedback.RateCap = d.Feedback.RateCap
	}
	if p.Feedback.RateWindow <= 0 {
		p.Feedback.RateWindow = d.Feedback.RateWindow
	}
	return p
}
