package fsx

// operation names a composite operation for the classification table.
type operation uint8

const (
	opReplace operation = iota + 1
	opReplaceDeferred
	opAppend
	opSyncFile
	opRenameFile
	opRemove
	opSyncDir
	opMakeDir
	opRenameDir
	opRemoveAll
	opDiscard
)

// classification is the one place that decides the class of a failed step.
//
// A step that fails before anything durable changed is Failed. Fatal is for
// the steps after which durability is unknown: a failed directory sync (the
// rename may or may not survive a crash, and Linux marks the pages clean after
// a failed write-back, so a retry can report success for lost data) and a
// failed sync of an appended file (the data is visible to this process but may
// never reach the disk; a later successful sync must not make the caller
// believe the whole batch is durable). A failed sync of the temporary file of
// a Replace is Failed: the target is untouched and nothing was acknowledged.
var classification = []struct {
	op    operation
	step  Step
	class Class
}{
	{opReplace, StepCreate, Failed},
	{opReplace, StepWrite, Failed},
	{opReplace, StepSync, Failed},
	{opReplace, StepRename, Failed},
	{opReplace, StepSyncDir, Fatal},
	{opReplaceDeferred, StepCreate, Failed},
	{opReplaceDeferred, StepWrite, Failed},
	{opReplaceDeferred, StepSync, Failed},
	{opReplaceDeferred, StepRename, Failed},

	{opAppend, StepOpen, Failed},
	{opAppend, StepWrite, Failed},

	{opSyncFile, StepSync, Fatal},
	{opSyncFile, StepSyncDir, Fatal},

	{opRenameFile, StepRename, Failed},
	{opRenameFile, StepSyncDir, Fatal},

	{opRemove, StepRemove, Failed},

	{opSyncDir, StepSyncDir, Fatal},

	{opMakeDir, StepMkdir, Failed},
	{opMakeDir, StepSyncDir, Fatal},

	{opRenameDir, StepRenameDir, Failed},
	{opRenameDir, StepSyncDir, Fatal},

	{opRemoveAll, StepRemoveAll, Failed},

	{opDiscard, StepRenameDir, Failed},
	{opDiscard, StepSyncDir, Fatal},
	{opDiscard, StepRemoveAll, Failed},
}

func classOf(op operation, step Step) Class {
	for _, r := range classification {
		if r.op == op && r.step == step {
			return r.class
		}
	}
	panic("fsx: no classification for a step of a composite operation")
}

// fail builds the error of a composite operation's failed step.
func fail(op operation, tag Tag, step Step, path string, err error) *Error {
	p := Point{Key: Key{Tag: tag, Step: step}}
	if observed, ok := PointOf(err); ok {
		p = observed
	}
	return &Error{Point: p, Path: path, Class: classOf(op, step), Err: err}
}

// reject builds the error of a call that was refused before any step ran:
// nothing was attempted, so nothing durable can have changed.
func reject(tag Tag, step Step, path string, err error) *Error {
	return &Error{Point: Point{Key: Key{Tag: tag, Step: step}}, Path: path, Class: Failed, Err: err}
}
