// Package swap replaces a set of installed programs by new ones so that the set
// changes all or not at all.
//
// The order is the one a running executable allows on every platform. Prepare
// writes each new program next to the program it replaces, as <target>.new.
// Commit then renames each current program to <target>.old, which Windows
// permits for a program that is running, and renames <target>.new to the
// target's name. When any step fails, every rename already made is undone.
// Clean removes what earlier runs left behind and puts back a program a crash
// took from its name. README.md has the reasons, the states a crash can leave
// and what the caller must guarantee.
//
// All paths are relative to the root of the fsx.FS the caller opened, which is
// the directory of the programs.
package swap

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
)

const (
	newSuffix = ".new"
	oldSuffix = ".old"
)

// programMode is the permission of a new program. The fsx.FS offers no other
// executable mode than owner-only.
const programMode = fsx.ModePrivateExec

// tagOf names the steps of the swap for fault injection and observation.
func tagOf(role string) fsx.Tag { return fsx.Tag{Tx: "swap", Role: role} }

// cleanupTag is the tag of removals whose failure changes nothing: the leftover
// is found and removed by the next Clean.
func cleanupTag() fsx.Tag { return fsx.Tag{Tx: fsx.TxCleanup} }

// Item is one program to replace.
type Item struct {
	Target string // path of the installed program in the destination FS
	Source string // path of the new program in the source Reader
}

// Swap is a set of new programs written next to their targets, ready to commit.
type Swap struct {
	fsys      fsx.FS
	targets   []string
	committed bool
}

// Prepare writes every item's Source as <Target>.new in the target's own
// directory. A <target>.new is only ever complete: it is written under a
// temporary name and renamed into place. When Prepare fails it has removed what
// it wrote.
func Prepare(dst fsx.FS, src fsx.Reader, items []Item) (*Swap, error) {
	if len(items) == 0 {
		return nil, errors.New("swap: nothing to replace")
	}
	s := &Swap{fsys: dst}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if seen[it.Target] {
			return nil, fmt.Errorf("swap: %s is listed twice", it.Target)
		}
		seen[it.Target] = true
		s.targets = append(s.targets, it.Target)
	}
	for _, it := range items {
		data, err := src.ReadFile(it.Source)
		if err == nil {
			err = fsx.ReplaceDeferred(dst, tagOf("new"), it.Target+newSuffix, data, programMode)
		}
		if err != nil {
			s.discardNew()
			return nil, fmt.Errorf("swap: prepare %s: %w", it.Target, err)
		}
	}
	if err := s.syncDirs("new"); err != nil {
		s.discardNew()
		return nil, fmt.Errorf("swap: prepare: %w", err)
	}
	return s, nil
}

// Commit puts the prepared programs in place: for each target in order, the
// current file is renamed to <target>.old and <target>.new is renamed to the
// target. If any step fails, including making the directory entries durable at
// the end, every target already moved is put back, the prepared files are
// removed, and the failure is returned: afterwards every target is as it was
// and no <target>.old remains. If putting a target back fails too, the error is
// a *RollbackError that names the <target>.old files holding the previous
// programs. After a successful Commit the <target>.old files stay, as the way
// back; Clean removes them in a later run.
//
// A Swap commits once.
func (s *Swap) Commit() error {
	if s.committed {
		return errors.New("swap: already committed")
	}
	s.committed = true
	var retired []string // targets whose current file is now <target>.old
	for _, t := range s.targets {
		if err := s.fsys.Rename(tagOf("retire"), t, t+oldSuffix); err != nil {
			return s.rollback(fmt.Errorf("swap: set aside %s: %w", t, err), retired)
		}
		retired = append(retired, t)
		if err := s.fsys.Rename(tagOf("install"), t+newSuffix, t); err != nil {
			return s.rollback(fmt.Errorf("swap: install %s: %w", t, err), retired)
		}
	}
	if err := s.syncDirs("install"); err != nil {
		return s.rollback(fmt.Errorf("swap: %w", err), retired)
	}
	return nil
}

// rollback puts every retired target back, newest first, and removes the files
// that were prepared and not installed. Renaming <target>.old over the target
// replaces an installed new program and restores one that was only set aside, so
// the same step serves both. The directories are not synced: a failed swap makes
// no promise about durability.
func (s *Swap) rollback(cause error, retired []string) error {
	failed := &RollbackError{Cause: cause}
	for i := len(retired) - 1; i >= 0; i-- {
		t := retired[i]
		if err := s.fsys.Rename(tagOf("restore"), t+oldSuffix, t); err != nil {
			failed.Held = append(failed.Held, t+oldSuffix)
			failed.Errs = append(failed.Errs, fmt.Errorf("swap: restore %s: %w", t, err))
		}
	}
	s.discardNew()
	if len(failed.Held) == 0 {
		return cause
	}
	return failed
}

// discardNew removes every <target>.new, those absent included. A failure is
// ignored: Clean removes what is left.
func (s *Swap) discardNew() {
	for _, t := range s.targets {
		fsx.Remove(s.fsys, cleanupTag(), t+newSuffix)
	}
}

// syncDirs makes the entries of every directory that holds a target durable.
func (s *Swap) syncDirs(role string) error {
	done := make(map[string]bool)
	for _, t := range s.targets {
		dir := path.Dir(t)
		if done[dir] {
			continue
		}
		done[dir] = true
		if err := fsx.SyncDir(s.fsys, tagOf(role), dir); err != nil {
			return err
		}
	}
	return nil
}

// RollbackError is the failure of a Commit that could not undo itself. Each of
// Held is a <target>.old file that holds the previous program, which is not
// back under its name.
type RollbackError struct {
	Cause error    // what made the swap fail
	Errs  []error  // the failed attempts to put programs back
	Held  []string // paths of the .old files that hold the previous programs
}

func (e *RollbackError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v; and the swap could not be undone", e.Cause)
	for _, err := range e.Errs {
		fmt.Fprintf(&b, "; %v", err)
	}
	for _, old := range e.Held {
		fmt.Fprintf(&b, "; the previous program is in %s: rename it to %s to restore it", old, strings.TrimSuffix(old, oldSuffix))
	}
	return b.String()
}

func (e *RollbackError) Unwrap() []error { return append([]error{e.Cause}, e.Errs...) }

// Cleaned is what a Clean did that its caller may want to say.
type Cleaned struct {
	// Restored lists the targets that were missing and are back, renamed from
	// <target>.old: a run stopped between the two renames of Commit.
	Restored []string
	// Left lists the files of earlier updates that could not be removed because a
	// process still runs from them, under the names they have now. Windows only.
	Left []string
}

// Clean removes what earlier runs left next to the targets, and puts back a
// program that a crash took from its name.
//
// A <target>.new is removed. A <target>.old whose target is missing is renamed
// back to the target and listed in Restored: a run stopped between the two
// renames of Commit, and the old program is the only copy. Otherwise a
// <target>.old is removed.
//
// On Windows a process that started from the old program keeps its file open;
// the file cannot be removed but can be renamed. Such a <target>.old is renamed
// to the first free <target>.old-<n> and listed in Left, so that the name .old
// is free for the next Commit and always means the manual rollback copy of the
// last update. Every Clean also tries to remove the <target>.old-<n> files that
// earlier ones set aside, and lists those still held. Any other failure is
// returned in err, after the remaining targets were cleaned.
//
// A crash while Prepare was writing can leave a temporary file named with the
// fsx.TempPrefix; Clean does not remove it, because it cannot tell it from the
// temporary file of another program in the same directory.
func Clean(fsys fsx.FS, targets []string) (Cleaned, error) {
	return clean(fsys, targets, fsx.IsHeld)
}

// clean is Clean with the judgment of which removal failures mean "a process
// runs from the file" given, so that its handling is tested on every platform.
func clean(fsys fsx.FS, targets []string, held func(error) bool) (Cleaned, error) {
	var cleaned Cleaned
	var errs []error
	for _, t := range targets {
		if err := cleanTarget(fsys, t, held, &cleaned); err != nil {
			errs = append(errs, err)
		}
	}
	return cleaned, errors.Join(errs...)
}

func cleanTarget(fsys fsx.FS, t string, held func(error) bool, cleaned *Cleaned) error {
	var errs []error
	if err := removeIfPresent(fsys, t+newSuffix); err != nil {
		errs = append(errs, err)
	}
	hasTarget, err := fsx.Exists(fsys, t)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	old := t + oldSuffix
	hasOld, err := fsx.Exists(fsys, old)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if !hasTarget {
		// Only a run that stopped between the two renames of Commit leaves a
		// target missing, and then <target>.old is the program.
		if hasOld {
			if err := fsys.Rename(tagOf("recover"), old, t); err != nil {
				errs = append(errs, fmt.Errorf("swap: restore %s from %s: %w", t, old, err))
			} else {
				cleaned.Restored = append(cleaned.Restored, t)
			}
		}
		return errors.Join(errs...)
	}
	if err := cleanSetAside(fsys, t, held, cleaned); err != nil {
		errs = append(errs, err)
	}
	if hasOld {
		err := fsx.Remove(fsys, tagOf("clean"), old)
		switch {
		case err == nil:
		case held(err):
			name, err := setAside(fsys, old)
			if err != nil {
				errs = append(errs, err)
			} else {
				cleaned.Left = append(cleaned.Left, name)
			}
		default:
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// cleanSetAside removes the <target>.old-<n> files that earlier runs of Clean
// renamed out of the way, and lists those that are still held.
func cleanSetAside(fsys fsx.FS, t string, held func(error) bool, cleaned *Cleaned) error {
	dir, base := path.Split(t)
	entries, err := fsys.ReadDir(path.Clean(dir))
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		n, ok := strings.CutPrefix(e.Name, base+oldSuffix+"-")
		if !ok || !isNumber(n) {
			continue
		}
		p := dir + e.Name
		switch err := fsx.Remove(fsys, tagOf("clean"), p); {
		case err == nil:
		case held(err):
			cleaned.Left = append(cleaned.Left, p)
		default:
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// setAside renames old to the first free <old>-<n>, n from 1, and returns the
// new name.
func setAside(fsys fsx.FS, old string) (string, error) {
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-%d", old, n)
		present, err := fsx.Exists(fsys, name)
		if err != nil {
			return "", err
		}
		if present {
			continue
		}
		if err := fsys.Rename(tagOf("hold"), old, name); err != nil {
			return "", fmt.Errorf("swap: %s cannot be removed while a process runs from it, and cannot be renamed: %w", old, err)
		}
		return name, nil
	}
}

func isNumber(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func removeIfPresent(fsys fsx.FS, p string) error {
	present, err := fsx.Exists(fsys, p)
	if err != nil || !present {
		return err
	}
	return fsx.Remove(fsys, tagOf("clean"), p)
}
