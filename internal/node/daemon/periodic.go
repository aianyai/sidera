package daemon

import (
	"fmt"
	"time"

	"github.com/aianyai/sidera/internal/node/engine"
	"github.com/aianyai/sidera/internal/node/keys"
)

// ClockStateEvery is how often the high water mark of the wall clock is made
// durable: a crash loses at most this much of it, which is harmless because the
// next start only trusts the clock less, never more.
const ClockStateEvery = time.Minute

// PeriodicTask is a task the engine runs every Every, the first time one period
// after the start. It runs in the loop, on the injected clock: it may write
// through the transaction layer and must not wait, and a task that has long work to do
// does it outside the loop, because the longest stall of the loop is what the
// notifications of an adapter have to outlast. Names are unique.
type PeriodicTask struct {
	Name  string
	Every time.Duration
	Run   engine.Task
}

const clockStateTask = "clock-high-water"

// CertificateCheckEvery is how often the certificate is looked at for renewal.
// The certificate lasts 30 days and is renewed with 10 or fewer left, so an hour
// is a small part of the margin: a certificate is never found expired for waiting
// for the next look, and a node that was down is brought up to date by the start.
const CertificateCheckEvery = time.Hour

const certificateTask = "certificate-renewal"

// ArchiveSweepEvery is how often the archive sweep runs. A delivery is swept about 27
// hours after its deadline and an administrative result or an acknowledged alert a
// week after they were made, so the sweep is never urgent. An hour keeps one pass to
// what a node at its upper scale (about 3,000 messages an hour) finishes in that
// time. A pass stalls the engine's loop once for a full scan of the files (about 3.2 s
// at 50,000 files and 11 to 13 s at 200,000, warm, as the storage reference measures),
// and then for a fraction of a second at a time while it removes in chunks; callers
// that meet the stall are told loop_busy and retry.
const ArchiveSweepEvery = time.Hour

// pauseClock remembers when the pause of a clock set back began. It is read and
// written only in the loop (the clock task) and, before the loop exists, by the
// start.
type pauseClock struct{ since time.Time }

// state is the clock state to make durable for what the loop reports. A pause
// keeps the time it began, which is the first the node saw: a state with another
// start time is a different state. When the owner clears the pause the time is
// forgotten, so that a pause that comes later begins at its own time and not at
// the one of a pause that is over.
func (p *pauseClock) state(in engine.TaskInput) keys.ClockState {
	st := keys.ClockState{HighWater: in.HighWater}
	if !in.RollbackPaused {
		p.since = time.Time{}
		return st
	}
	if p.since.IsZero() {
		p.since = in.Now
	}
	st.RollbackPausedSince = p.since
	return st
}

// clockTask makes the high water mark and the pause durable. The owner's clearing
// of the pause is the only thing that lifts it.
func (d *Daemon) clockTask(in engine.TaskInput) error {
	return d.keys.SaveClock(d.pause.state(in))
}

// certificateTask is the hourly look at the certificate. It runs in the loop, as
// does everything that signs, and the clock it judges by is the one of the command.
// Whether it may sign is the engine's judgement and nothing else.
func (d *Daemon) certificateTask(in engine.TaskInput) error {
	return d.renewCertificate(in.Now, in.SignRefusal)
}

// renewCertificate renews the certificate when the keys say it is due, and never
// while the engine says the node may not sign (refusal, zero when it may). A
// certificate carries the time it was signed at, so one signed on a clock that was
// set back would start before what the node already issued; and a node whose
// identity was retired signs nothing, although the loop runs until the daemon is
// down. A certificate that is due and waits for the owner to clear the pause is
// said so in the log; the check after the clear signs it. An error is the keys':
// a fatal one ends the node, any other is tried again by the engine.
func (d *Daemon) renewCertificate(now time.Time, refusal engine.Reason) error {
	verdict, reason := d.keys.RenewalDue(now)
	if verdict != keys.RenewDue {
		return nil
	}
	switch refusal {
	case 0:
	case engine.ReasonClockRollback:
		d.log.Warn("the certificate is due and is not signed while the wall clock is in doubt; clear the pause to renew it")
		return nil
	default:
		d.log.Warn("the certificate is due and is not signed", "reason", refusal.String())
		return nil
	}
	if err := d.keys.Renew(now); err != nil {
		return fmt.Errorf("renewing the certificate: %w", err)
	}
	d.log.Info("the certificate was renewed", "serial", d.keys.SerialSeen().Serial, "reason", renewReasonName(reason))
	return nil
}

// renewReasonName says why a renewal was made, for the log.
func renewReasonName(r keys.RenewReason) string {
	switch r {
	case keys.RenewReasonInterrupted:
		return "an earlier renewal was interrupted"
	case keys.RenewReasonExpiring:
		return "ten days or less were left"
	}
	return "unknown"
}
