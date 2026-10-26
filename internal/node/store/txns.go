package store

import "github.com/aianyai/sidera/internal/node/txn"

// Role names a transaction of another package uses for the files of state/ it
// asks this package to write, with the write functions PutEndpoint and
// PutAdminResult (their values come from PrepareEndpoint and
// PrepareAdminResult, called before the transaction begins). A role name is part of the crash point identifiers, so it is
// fixed.
const (
	// RoleEndpoint is the role of an endpoint record.
	RoleEndpoint = "endpoint"
	// RoleResult is the role of the result of an administrative operation.
	RoleResult = "result"
)

// Shorthands for the declarations below: a write is {Role, Op, flags}.
func rep(role string, flags ...func(*txn.Write)) txn.Write { return w(role, txn.OpReplace, flags...) }
func repDefer(role string, flags ...func(*txn.Write)) txn.Write {
	return w(role, txn.OpReplaceDefer, flags...)
}
func syncDir(role string, flags ...func(*txn.Write)) txn.Write {
	return w(role, txn.OpSyncDir, flags...)
}
func rm(role string, flags ...func(*txn.Write)) txn.Write { return w(role, txn.OpRemove, flags...) }

func w(role string, op txn.Op, flags ...func(*txn.Write)) txn.Write {
	x := txn.Write{Role: role, Op: op}
	for _, f := range flags {
		f(&x)
	}
	return x
}

func many(x *txn.Write)     { x.Many = true }
func optional(x *txn.Write) { x.Optional = true }
func prepare(x *txn.Write)  { x.Prepare = true }

// The declarations of the transactions this package owns. Each is a literal;
// nothing registers them at run time. A transaction that writes the files of
// two owners is declared by the package that opens it (the engine, the
// initialization entry), which prepares before it begins and calls PutEndpoint
// and PutAdminResult with the roles above.
var (
	// intake: a message accepted from a local sender. The body is written first
	// and is garbage until the record that refers to it is there.
	declIntake = txn.Decl{Name: "intake", Kind: txn.Normal, Writes: []txn.Write{
		rep("body", prepare, optional), rep("out"),
	}}
	// deliver: a message accepted on the target. A rejected message has no body.
	declDeliver = txn.Decl{Name: "deliver", Kind: txn.Normal, Writes: []txn.Write{
		rep("body", prepare, optional), repDefer("in", many), syncDir("in-dir"),
	}}
	// deliver-reply: a reply that carries evidence. The source record is
	// updated before the target record that depends on it, and a body whose last
	// reference the evidence dropped is removed at the end. The reply's own body
	// has a role of its own: a role names one write of a transaction, and the
	// removal of released bodies is the "body" of the other transactions.
	declDeliverReply = txn.Decl{Name: "deliver-reply", Kind: txn.Normal, Writes: []txn.Write{
		rep("reply-body", prepare, optional), rep("out", optional), repDefer("in", many), syncDir("in-dir"),
		rm("body", many, optional), syncDir("body-dir", optional),
	}}
	// source-update: one source record changes; the body is removed when the
	// last reference went.
	declSourceUpdate = txn.Decl{Name: "source-update", Kind: txn.Normal, Writes: []txn.Write{
		rep("out"), rm("body", many, optional), syncDir("body-dir", optional),
	}}
	// control-intake: a control signal accepted on the target. A session
	// endpoint and a body are preparation: without the control record that
	// refers to them they are garbage.
	declControlIntake = txn.Decl{Name: "control-intake", Kind: txn.Normal, Writes: []txn.Write{
		rep("body", prepare, optional), rep("endpoint", prepare, optional), rep("ctl"),
	}}
	// control-result: the result of a control signal, then the endpoint it
	// moves, then the body that is no longer referenced.
	declControlResult = txn.Decl{Name: "control-result", Kind: txn.Normal, Writes: []txn.Write{
		rep("ctl"), rep("endpoint", optional), rm("body", many, optional), syncDir("body-dir", optional),
	}}
	// control-reply: the source handles a control reply.
	declControlReply = txn.Decl{Name: "control-reply", Kind: txn.Normal, Writes: []txn.Write{
		rep("out"),
	}}
	declPromptBegin = txn.Decl{Name: "prompt-begin", Kind: txn.Normal, Writes: []txn.Write{
		rep("attempts"),
	}}
	declPromptEnd = txn.Decl{Name: "prompt-end", Kind: txn.Normal, Writes: []txn.Write{
		rep("attempts"), repDefer("in", many, optional), syncDir("in-dir", optional),
	}}
	declBlocked = txn.Decl{Name: "blocked", Kind: txn.Normal, Writes: []txn.Write{
		rep("attempts"), repDefer("in", many, optional), syncDir("in-dir", optional),
	}}
	declLease = txn.Decl{Name: "lease", Kind: txn.Normal, Writes: []txn.Write{
		repDefer("in", many), syncDir("in-dir"),
	}}
	declAck = txn.Decl{Name: "ack", Kind: txn.Normal, Writes: []txn.Write{
		repDefer("in", many), syncDir("in-dir"), rm("body", many, optional), syncDir("body-dir", optional),
	}}
	declTick = txn.Decl{Name: "tick", Kind: txn.Normal, Writes: []txn.Write{
		repDefer("out", many, optional), syncDir("out-dir", optional),
		repDefer("in", many, optional), syncDir("in-dir", optional),
		repDefer("ctl", many, optional), syncDir("ctl-dir", optional),
		rm("body", many, optional), syncDir("body-dir", optional),
	}}
	declEndpointUpdate = txn.Decl{Name: "endpoint-update", Kind: txn.Normal, Writes: []txn.Write{
		rep("endpoint"),
	}}
	// endpoint-close: the head record of a closure. The queued deliveries are
	// closed by endpoint-drain, in chunks.
	declEndpointClose = txn.Decl{Name: "endpoint-close", Kind: txn.Normal, Writes: []txn.Write{
		rep("endpoint"), rep("result", optional),
	}}
	declEndpointDrain = txn.Decl{Name: "endpoint-drain", Kind: txn.Normal, Writes: drainWrites(nil)}
	declInstanceDrain = txn.Decl{Name: "instance-drain", Kind: txn.Normal, Writes: drainWrites(instanceHead())}
	declFeedbackForm  = txn.Decl{Name: "feedback-form", Kind: txn.Normal, Writes: []txn.Write{rep("body", prepare), rep("notice")}}
	declFeedbackClear = txn.Decl{Name: "feedback-clear", Kind: txn.Normal, Writes: []txn.Write{repDefer("out", many), syncDir("out-dir")}}
	declCheckpointPut = txn.Decl{Name: "checkpoint-put", Kind: txn.Normal, Writes: []txn.Write{rep("checkpoint")}}
	declAlertRaise    = txn.Decl{Name: "alert-raise", Kind: txn.Normal, Writes: []txn.Write{rep("alert")}}
	declArchiveEvents = txn.Decl{Name: "archive-events", Kind: txn.Normal, Writes: []txn.Write{w("archive", txn.OpAppend, many)}}
	declArchiveSweep  = txn.Decl{Name: "archive-sweep", Kind: txn.Normal, Writes: []txn.Write{
		w("archive", txn.OpAppend, many, optional), w("archive", txn.OpSyncFile, optional),
		rep("seq-floor", many, optional),
		rm("ctl", many, optional), syncDir("ctl-dir", optional),
		rm("record", many, optional), rm("body", many, optional), syncDir("dir", many, optional),
		rm("endpoint", many, optional), syncDir("endpoint-dir", optional),
	}}
	declArchiveExpire = txn.Decl{Name: "archive-expire", Kind: txn.Normal, Writes: []txn.Write{rm("file", many), syncDir("dir")}}
	declAlertAck      = txn.Decl{Name: "alert-ack", Kind: txn.Normal, Writes: []txn.Write{rep("alert"), rep("result")}}
	declDaemonStop    = txn.Decl{Name: "daemon-stop", Kind: txn.Normal, Writes: []txn.Write{rep("result")}}

	// The recovery transactions.
	//
	// quarantine settles one damaged file: the sequence floors it raises, the
	// session endpoints it closes and the alert come first, and the damaged file
	// goes last, overwritten in place or removed. A restart that finds it still
	// there writes the same effects again.
	declQuarantine = txn.Decl{Name: "quarantine", Kind: txn.Recovery, Writes: []txn.Write{
		rep("seq-floor", optional), rep("session", many, optional), rep("alert"),
		rep("overwrite", optional), rm("record", optional), syncDir("dir", optional),
	}}
	declOrphanGC            = txn.Decl{Name: "orphan-gc", Kind: txn.Recovery, Writes: []txn.Write{rm("body", many, optional), syncDir("dir", many)}}
	declOrphanGCEndpoint    = txn.Decl{Name: "orphan-gc-endpoint", Kind: txn.Recovery, Writes: []txn.Write{rm("endpoint", many, optional), syncDir("dir", many)}}
	declCtlEffectReplay     = txn.Decl{Name: "ctl-effect-replay", Kind: txn.Recovery, Writes: []txn.Write{repDefer("endpoint", many, optional), syncDir("endpoint-dir", optional)}}
	declEndpointDrainReplay = txn.Decl{Name: "endpoint-drain-replay", Kind: txn.Recovery, Writes: drainWrites(nil)}
	declInstanceDrainReplay = txn.Decl{Name: "instance-drain-replay", Kind: txn.Recovery, Writes: drainWrites(instanceHead())}
	declPromptReplay        = txn.Decl{Name: "prompt-replay", Kind: txn.Recovery, Writes: []txn.Write{repDefer("in", many), syncDir("in-dir")}}
	declBlockedReplay       = txn.Decl{Name: "blocked-replay", Kind: txn.Recovery, Writes: []txn.Write{repDefer("in", many), syncDir("in-dir")}}
	declFeedbackReplay      = txn.Decl{Name: "feedback-replay", Kind: txn.Recovery, Writes: []txn.Write{repDefer("out", many), syncDir("out-dir")}}
)

// instanceHead is the part of instance-drain before the part it shares with
// endpoint-drain: the endpoints of the instance are closed first.
func instanceHead() []txn.Write {
	return []txn.Write{repDefer("endpoint", many), syncDir("endpoint-dir")}
}

// drainWrites is the declaration of closing the deliveries queued for closed
// endpoints, after an optional head: the target records, the control records,
// and the bodies nothing refers to any more.
func drainWrites(head []txn.Write) []txn.Write {
	return append(append([]txn.Write(nil), head...),
		repDefer("in", many, optional), syncDir("in-dir", optional),
		repDefer("ctl", many, optional), syncDir("ctl-dir", optional),
		rm("body", many, optional), syncDir("body-dir", optional),
	)
}

// Transactions returns the declarations of every transaction this package
// owns, for the composition root to put in the table of the program.
func Transactions() []*txn.Decl {
	return []*txn.Decl{
		&declIntake, &declDeliver, &declDeliverReply, &declSourceUpdate, &declControlIntake,
		&declControlResult, &declControlReply, &declPromptBegin, &declPromptEnd, &declBlocked,
		&declLease, &declAck, &declTick, &declEndpointUpdate, &declEndpointClose,
		&declEndpointDrain, &declInstanceDrain, &declFeedbackForm, &declFeedbackClear,
		&declCheckpointPut, &declAlertRaise, &declArchiveEvents, &declArchiveSweep,
		&declArchiveExpire, &declAlertAck, &declDaemonStop,
		&declQuarantine, &declOrphanGC,
		&declOrphanGCEndpoint, &declCtlEffectReplay, &declEndpointDrainReplay,
		&declInstanceDrainReplay, &declPromptReplay, &declBlockedReplay, &declFeedbackReplay,
	}
}
