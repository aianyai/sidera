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
	// before it sends the same request again, on the injected clock. A message is
	// sent again until its delivery deadline; the target deduplicates by sender
	// node, sender endpoint, message id and recipient and compares the request
	// digest, so a send that was processed and whose receipt was lost is harmless.
	ResendBackoff time.Duration
	// PullInterval is how long a sender waits between two pulls of the reports of
	// a delivery that is not final, until its report deadline.
	PullInterval time.Duration
	// CreateReplyDeadline is how long a session creation has, from the moment the
	// node accepted the signal, to show the session ready: when it ends the sender
	// is told that the creation is unconfirmed. Until then a call that failed or
	// was not answered is made again.
	CreateReplyDeadline time.Duration
	// CreateRetryBackoff is how long a creation waits before the call to the
	// adapter is made again after one that failed or was answered as
	// indeterminate, on the injected clock.
	CreateRetryBackoff time.Duration
	// ControlReplyDeadline is how long a close or an interrupt has, from the moment
	// the node accepted the signal, to be carried out by the adapter: a call that
	// failed or got no answer is made again until then, and then the sender is told
	// the outcome could not be confirmed.
	ControlReplyDeadline time.Duration
	// InterruptConfirmWait is how long the node waits for the end of the turn after
	// the adapter said it asked the runtime to stop it, before it tells the sender
	// that the interrupt was requested and not confirmed.
	InterruptConfirmWait time.Duration
	// PromptCallDeadline is how long a call to an adapter about a prompt
	// (prompt.deliver or prompt.reconcile) may take, on the injected clock: when it
	// ends the call is cancelled and the attempt takes the outcome of a call that got
	// no answer, so that an adapter that hangs cannot keep an endpoint busy, and a turn
	// that never started cannot hold a place of the node's cap, for ever.
	PromptCallDeadline time.Duration
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

		CreateReplyDeadline: 3 * time.Minute,
		CreateRetryBackoff:  30 * time.Second,

		ControlReplyDeadline: 2 * time.Minute,
		InterruptConfirmWait: 30 * time.Second,
		PromptCallDeadline:   time.Minute,
		Feedback:             delivery.DefaultFeedbackParams(),
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
	if p.CreateReplyDeadline <= 0 {
		p.CreateReplyDeadline = d.CreateReplyDeadline
	}
	if p.CreateRetryBackoff <= 0 {
		p.CreateRetryBackoff = d.CreateRetryBackoff
	}
	if p.ControlReplyDeadline <= 0 {
		p.ControlReplyDeadline = d.ControlReplyDeadline
	}
	if p.PromptCallDeadline <= 0 {
		p.PromptCallDeadline = d.PromptCallDeadline
	}
	if p.InterruptConfirmWait <= 0 {
		p.InterruptConfirmWait = d.InterruptConfirmWait
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
