package store

import "path"

// Kind is the kind of a record file: the first field after the magic of its
// first line, and the part of its file name hash that tells apart records that
// share a key text.
type Kind string

// The kinds of the files of state/. A kind belongs to one directory, except
// that out/ holds three: a message and a control signal share a key text but are
// different records, and the kind is in the file name hash so that damage to
// the kind cannot turn one into the other. The third is the notice that a file
// of out/ was lost without a trace of what it held (OutLostRecord).
const (
	KindOut        Kind = "out"
	KindOutCtl     Kind = "out-ctl"
	KindOutLost    Kind = "out-lost"
	KindIn         Kind = "in"
	KindCtl        Kind = "ctl"
	KindEndpoint   Kind = "endpoint"
	KindAttempts   Kind = "attempts"
	KindCheckpoint Kind = "checkpoint"
	KindAdmin      Kind = "admin"
	KindAlert      Kind = "alert"
	KindSeq        Kind = "seq"
)

// Directories of state/, relative to the data directory root.
const (
	StateDir       = "state"
	OutDir         = "state/out"
	InDir          = "state/in"
	CtlDir         = "state/ctl"
	BodiesDir      = "state/bodies"
	EndpointsDir   = "state/endpoints"
	AttemptsDir    = "state/attempts"
	CheckpointsDir = "state/checkpoints"
	AdminDir       = "state/admin"
	AlertsDir      = "state/alerts"
	SeqDir         = "state/seq"
	ArchiveDir     = "state/archive"
)

// StateDirs lists every directory of state/, a parent before its children: the
// layout a start rebuilds when state/ or one of its directories is missing.
func StateDirs() []string {
	return []string{StateDir, OutDir, InDir, CtlDir, BodiesDir, EndpointsDir, AttemptsDir, CheckpointsDir, AdminDir, AlertsDir, SeqDir, ArchiveDir}
}

// recordDirs are the directories that hold record files, in the order a scan
// reads them, and so the order Scan.Damaged lists them in. The floor files come
// first: quarantining a source record reads the floor file of its sender, and
// refuses a damaged one, so a damaged floor file must be settled before the
// records that need it.
var recordDirs = []string{SeqDir, OutDir, InDir, CtlDir, EndpointsDir, AttemptsDir, CheckpointsDir, AdminDir, AlertsDir}

// kindDir is the directory of each kind.
var kindDir = map[Kind]string{
	KindOut: OutDir, KindOutCtl: OutDir, KindOutLost: OutDir, KindIn: InDir, KindCtl: CtlDir,
	KindEndpoint: EndpointsDir, KindAttempts: AttemptsDir, KindCheckpoint: CheckpointsDir,
	KindAdmin: AdminDir, KindAlert: AlertsDir, KindSeq: SeqDir,
}

// Dir is the directory of the kind, "" for a kind that is not one of state/.
func (k Kind) Dir() string { return kindDir[k] }

// kindsOf lists the kinds a directory may hold.
func kindsOf(dir string) []Kind {
	var out []Kind
	for _, k := range []Kind{KindOut, KindOutCtl, KindOutLost, KindIn, KindCtl, KindEndpoint, KindAttempts, KindCheckpoint, KindAdmin, KindAlert, KindSeq} {
		if kindDir[k] == dir {
			out = append(out, k)
		}
	}
	return out
}

// RecordPath is the path of a record file, from its directory and its name.
func RecordPath(dir, name string) string { return path.Join(dir, name) }
