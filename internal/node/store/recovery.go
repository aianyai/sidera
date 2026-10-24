package store

import "github.com/aianyai/sidera/internal/node/fsx"

// Recovery rules. Each rule has a predicate, a pure function of a Scan that
// names the work, and a method that does it. The predicate is the one thing the
// rule and the check "a run without a crash or damage has no recovery work" both
// call, so the check cannot drift from the rule. The predicates of the rules
// that need the delivery rules to decide what to write (draining records of a
// closed endpoint, applying the event of a prompt or a block) return the
// candidates and the engine applies the transition.

// MissingStateDirs lists the directories of state/ that do not exist, parent
// first: the work of the layout rule.
func MissingStateDirs(r fsx.Reader) ([]string, error) { panic(errNotImplemented) }

// OrphanBodies is the work of orphan-gc: the bodies no record refers to. A
// record refers to the body it holds a reference to; a quarantined file refers
// to nothing.
func OrphanBodies(sc *Scan) []string { panic(errNotImplemented) }

// OrphanEndpoints is the work of orphan-gc-endpoint: the session endpoints
// still being created whose control record is neither there nor quarantined. The
// endpoint is preparation that never became part of a committed control record.
func OrphanEndpoints(sc *Scan) []EndpointRecord { panic(errNotImplemented) }

// UnfinishedQuarantines is the work of quarantine-resume: the quarantined files
// of the directories of records whose alert does not exist. The alert is the
// last write of a quarantine, so a quarantine is finished exactly when it is
// there.
func UnfinishedQuarantines(sc *Scan) []CorruptFile { panic(errNotImplemented) }

// UnfinishedBodyQuarantines is the work of the continuation of quarantine-body:
// the quarantined bodies whose alert does not exist.
func UnfinishedBodyQuarantines(sc *Scan) []CorruptFile { panic(errNotImplemented) }

// EffectReplays is the work of ctl-effect-replay, as the endpoint records to
// write: a session endpoint still being created whose control record holds the
// end of the creation becomes open or closed accordingly, and an open session
// endpoint a control record closed becomes closed. Nothing is ever moved from
// closed to open.
func EffectReplays(sc *Scan) []EndpointRecord { panic(errNotImplemented) }

// DrainCandidates is the structural part of the work of endpoint-drain-replay:
// the target and control records still accepted, not lost, whose endpoint is
// closed or has no record. The engine decides, with the delivery rules, which
// of them a forced close changes.
func DrainCandidates(sc *Scan) (in []InRecord, ctl []CtlRecord) { panic(errNotImplemented) }

// InstanceDrainWork is the work of instance-drain-replay: the open endpoints
// of instances for which enabled is false, as closed records, token removed.
func InstanceDrainWork(sc *Scan, enabled func(instance string) bool) []EndpointRecord {
	panic(errNotImplemented)
}

// PendingWork is one head record with the followers it still owes: the target
// records that exist, were not written since the head, and are the same record.
type PendingWork struct {
	Head      AttemptsRecord
	Followers []InRecord
}

// PromptReplays is the work of prompt-replay.
func PromptReplays(sc *Scan) []PendingWork { panic(errNotImplemented) }

// BlockedReplays is the work of blocked-replay: the pending lists of blocks and
// of recoveries.
func BlockedReplays(sc *Scan) []PendingWork { panic(errNotImplemented) }

// FeedbackReplays is the work of feedback-replay, as the source records to
// write: those whose pending feedback holds items a notice already covers,
// without those items.
func FeedbackReplays(sc *Scan) []OutRecord { panic(errNotImplemented) }

// Quarantine is the quarantine of one damaged file.
type Quarantine struct {
	File DamagedFile
	// Alert is the alert of the quarantine, its ID chosen before and made part
	// of the quarantined name.
	Alert AlertRecord
	// Replacement is the record to put under the original name, for a damaged
	// delivery record whose key can be trusted; nil otherwise.
	Replacement Record
	// Session is the closed record of the session endpoint still being created
	// whose control record this file was; nil otherwise.
	Session *EndpointRecord
	// Scan is the scan the quarantine is part of: the other source records of a
	// pair set the sequence floor.
	Scan *Scan
}

// Quarantine renames the damaged file, raises the sequence floors of a source
// record whose numbers are lost, puts the replacement, closes the session
// endpoint, and writes the alert last.
func (s *Store) Quarantine(q Quarantine) error { panic(errNotImplemented) }

// QuarantineResume finishes a quarantine whose alert is missing: what was
// there is not written again, the floors are raised once more, and the alert
// is written.
type QuarantineResume struct {
	File        CorruptFile
	Alert       AlertRecord
	Replacement Record
	Session     *EndpointRecord
	Scan        *Scan
}

// QuarantineResume writes what the interrupted quarantine of the file did not.
func (s *Store) QuarantineResume(q QuarantineResume) error { panic(errNotImplemented) }

// BodyQuarantine is one chunk of the quarantine of a body whose content does
// not match its name.
type BodyQuarantine struct {
	Name    string  // the body file
	AlertID AlertID // part of the quarantined name
	// Rename is set on the first chunk: the body is renamed before anything
	// else, which makes the head record of the transaction.
	Rename bool
	// Out, In and Ctl are the records that refer to the body, each replaced by
	// a lost record or stripped of the reference.
	Out []OutRecord
	In  []InRecord
	Ctl []CtlRecord
	// Alert is set on the last chunk.
	Alert *AlertRecord
}

// QuarantineBody writes a chunk of the quarantine of a body.
func (s *Store) QuarantineBody(q BodyQuarantine) error { panic(errNotImplemented) }

// OrphanGC removes the bodies, then syncs the directory.
func (s *Store) OrphanGC(bodies []string) error { panic(errNotImplemented) }

// OrphanGCEndpoint removes the endpoint records, then syncs the directory.
func (s *Store) OrphanGCEndpoint(eps []EndpointRecord) error { panic(errNotImplemented) }

// CtlEffectReplay writes the endpoint records EffectReplays returned.
func (s *Store) CtlEffectReplay(eps []EndpointRecord) error { panic(errNotImplemented) }

// EndpointDrainReplay writes a chunk of the closed deliveries of closed
// endpoints.
func (s *Store) EndpointDrainReplay(b DrainBatch) error { panic(errNotImplemented) }

// InstanceDrainReplay closes the endpoints of disabled instances and writes a
// chunk of their deliveries.
func (s *Store) InstanceDrainReplay(closed []EndpointRecord, b DrainBatch) error {
	panic(errNotImplemented)
}

// PromptReplay writes the followers of a prompt, with the event applied.
func (s *Store) PromptReplay(ins []InRecord) error { panic(errNotImplemented) }

// BlockedReplay writes the followers of a block or a recovery.
func (s *Store) BlockedReplay(ins []InRecord) error { panic(errNotImplemented) }

// FeedbackReplay writes the source records without the feedback a notice covers.
func (s *Store) FeedbackReplay(outs []OutRecord) error { panic(errNotImplemented) }
