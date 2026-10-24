package store

import (
	"time"
)

// AdminResultRetention is how long the result of an administrative operation is
// kept: long enough for the command line to resend an operation whose outcome it
// did not learn and for the owner to ask for a result after a weekend.
const AdminResultRetention = 7 * 24 * time.Hour

// AlertAckRetention is how long an acknowledged alert is kept.
const AlertAckRetention = 7 * 24 * time.Hour

// RetainUntil is the instant until which the records of a delivery are kept as
// live files, and whether they are finished: every delivery in a final state.
// It is the latest of the retentions the delivery and reply packages define
// for the facts the record holds. A record that is not finished is never swept.
func RetainUntil(rec Record) (until time.Time, finished bool) { panic(errNotImplemented) }

// SweepPlan is the work of one pass of the archive sweep.
type SweepPlan struct {
	Summaries []ArchiveSummary
	Corrupt   []FileRef
	Ctl       []FileRef
	Records   []FileRef
	Bodies    []BodyRef
	// Outs are the source records among the removals, for the sequence floors.
	Outs []OutRecord
}

// PlanSweep selects what is ready to leave the live files at now: records that
// are finished and past their retention, with their quarantined files first and
// a session's derived delivery after its control record; admin results past
// theirs; acknowledged alerts whose quarantined file is gone; closed endpoints
// nothing live points to; and the bodies nothing refers to afterwards.
func PlanSweep(sc *Scan, now time.Time) SweepPlan { panic(errNotImplemented) }
