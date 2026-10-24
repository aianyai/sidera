package txn

import "github.com/aianyai/sidera/internal/node/fsx"

// stepsOf is the steps one call of op runs, in order; the keys of a table are
// expanded from it. For OpAppend the open is taken only by the first call on a
// path. It returns nil for a value that is not an operation.
func stepsOf(op Op) []fsx.Step {
	switch op {
	case OpReplace:
		return []fsx.Step{fsx.StepCreate, fsx.StepWrite, fsx.StepSync, fsx.StepRename, fsx.StepSyncDir}
	case OpReplaceDefer:
		return []fsx.Step{fsx.StepCreate, fsx.StepWrite, fsx.StepSync, fsx.StepRename}
	case OpAppend:
		return []fsx.Step{fsx.StepOpen, fsx.StepWrite}
	case OpSyncFile:
		return []fsx.Step{fsx.StepSync, fsx.StepSyncDir}
	case OpRenameFile:
		return []fsx.Step{fsx.StepRename, fsx.StepSyncDir}
	case OpRemove:
		return []fsx.Step{fsx.StepRemove}
	case OpSyncDir:
		return []fsx.Step{fsx.StepSyncDir}
	case OpMakeDir:
		return []fsx.Step{fsx.StepMkdir, fsx.StepSyncDir}
	case OpRenameDir:
		return []fsx.Step{fsx.StepRenameDir, fsx.StepSyncDir}
	case OpRemoveAll:
		return []fsx.Step{fsx.StepRemoveAll}
	case OpDiscard:
		return []fsx.Step{fsx.StepRenameDir, fsx.StepSyncDir, fsx.StepRemoveAll}
	}
	return nil
}

// commitStepOf is the step of op whose completion, in the first write of a
// transaction that is not preparatory, is the transaction's commit point; zero
// when the operation has none. It is the first step that other readers of the
// disk, and this process's own later reads, can see: a namespace step (rename,
// remove, mkdir, rename-dir, remove-all). A SyncFile and a lone SyncDir have no
// namespace step, so their sync is it. An Append has neither: the bytes it
// writes are not durable until a SyncFile, and a reader skips the damaged
// record a failed append leaves, so a failed append never needs to be fatal.
//
// This is the only place the commit point is defined.
func commitStepOf(op Op) fsx.Step {
	switch op {
	case OpReplace, OpReplaceDefer, OpRenameFile:
		return fsx.StepRename
	case OpRemove:
		return fsx.StepRemove
	case OpMakeDir:
		return fsx.StepMkdir
	case OpRenameDir, OpDiscard:
		return fsx.StepRenameDir
	case OpRemoveAll:
		return fsx.StepRemoveAll
	case OpSyncFile:
		return fsx.StepSync
	case OpSyncDir:
		return fsx.StepSyncDir
	}
	return 0
}

// commitStepCompleted reports whether the commit step of op had completed when
// a call of op failed at step: the failure came after it in the operation's
// sequence. A failure of the commit step itself did not complete it.
func commitStepCompleted(op Op, failed fsx.Step) bool {
	commit := commitStepOf(op)
	if commit == 0 {
		return false
	}
	steps := stepsOf(op)
	return indexOf(steps, failed) > indexOf(steps, commit)
}

func indexOf(steps []fsx.Step, s fsx.Step) int {
	for i, x := range steps {
		if x == s {
			return i
		}
	}
	return -1
}
