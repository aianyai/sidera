package crashwalk

import (
	"sort"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// Target is where one run kills the system.
type Target struct {
	// Gen is the process generation to kill: 1 for the first process, 2 for the
	// process that restarts after a crash.
	Gen int
	// Point is the crash point: the system dies just before it runs. The zero
	// value for a kill at a step boundary.
	Point fsx.Point
	// AfterStep, when set, kills the system from outside after the named script
	// step has finished and the system has gone quiet.
	AfterStep string
}

// TriggerKind says how the state a recovery case needs is made.
type TriggerKind int

const (
	// TriggerCrashAt: the first process is killed at a crash point, which leaves
	// work for the recovery.
	TriggerCrashAt TriggerKind = iota + 1
	// TriggerCorrupt: while the system is stopped a file is damaged, which is the
	// only way to make the damage a recovery repairs.
	TriggerCorrupt
)

// Trigger makes the state a recovery case starts from.
type Trigger struct {
	Kind TriggerKind
	// CrashAt is the crash point of TriggerCrashAt.
	CrashAt fsx.Point
	// Pattern names the files, relative to the data directory, and Damage says what
	// is done to them, for TriggerCorrupt. Damage is a description the backend
	// understands; the walk does not read it.
	Pattern string
	Damage  string
}

// CrashAt is the trigger that kills the first process at a crash point.
func CrashAt(p fsx.Point) Trigger { return Trigger{Kind: TriggerCrashAt, CrashAt: p} }

// CorruptOffline is the trigger that damages files while the system is stopped.
func CorruptOffline(pattern, damage string) Trigger {
	return Trigger{Kind: TriggerCorrupt, Pattern: pattern, Damage: damage}
}

// RecoveryCase is one write of a recovery transaction a scenario is responsible
// for, and how to bring the system to a state in which the recovery writes it.
type RecoveryCase struct {
	// Item is the write: transaction, role and step.
	Item fsx.Key
	// After is how the state that makes the recovery write it comes about.
	After Trigger
}

// Scenario is what the walk needs to know of a script. The script itself is the
// backend's.
// Scenario.Steps names only post-action boundaries with a live process that
// the backend can actually interrupt. Completed commands and terminal operations
// have no after-return boundary.
type Scenario interface {
	// Name identifies the scenario in reports.
	Name() string
	// Steps are the names of the script's steps, in order, for kills at step
	// boundaries.
	Steps() []string
	// MaxPoints is the most main crash points the scenario's window may have. A
	// scenario that writes more fails the walk, so that growth is seen by a
	// reviewer instead of being sampled away.
	MaxPoints() int
	// Recovery lists the recovery writes this scenario crashes, each with its
	// trigger.
	Recovery() []RecoveryCase
}

// window returns the hits of the first generation made after the setup.
func window(rec []Hit) []Hit {
	var out []Hit
	for _, h := range rec {
		if h.Gen == 1 && !h.Setup {
			out = append(out, h)
		}
	}
	return out
}

// Plan returns the targets of the first-generation runs of a scenario, from the
// hits of its clean recording.
//
// Full takes every point of the window except the sync steps, which add nothing
// to a process kill, then a kill after every step in steps. Smoke takes one point
// per transaction: the first time it reaches the visible step of the last file it
// writes before its commit, which is the point at which the transaction is most
// nearly done and not done.
//
// The targets come in a canonical order that does not depend on the recording's
// interleaving: the points sorted by key and then by hit number, then the step
// boundaries in the order of steps. A shard numbers the targets of a scenario by
// this order, so two shards whose recordings interleave writes of different
// transactions differently still agree on which target is number k.
func Plan(rec []Hit, mode Mode, steps []string) []Target {
	win := window(rec)
	var out []Target
	if mode == Full {
		for _, h := range win {
			if !IsSync(h.Point.Step) {
				out = append(out, Target{Gen: 1, Point: h.Point})
			}
		}
	} else {
		out = smoke(win)
	}
	sort.Slice(out, func(i, j int) bool { return pointLess(out[i].Point, out[j].Point) })
	if mode == Full {
		for _, s := range steps {
			out = append(out, Target{Gen: 1, AfterStep: s})
		}
	}
	return out
}

// pointLess orders crash points by key and then by hit number, so that hit 10
// follows hit 9.
func pointLess(a, b fsx.Point) bool {
	if ka, kb := a.Key.String(), b.Key.String(); ka != kb {
		return ka < kb
	}
	return a.Hit < b.Hit
}

// smoke picks, for each transaction in the order it first appears, the point
// before its last file becomes visible.
func smoke(win []Hit) []Target {
	var order []string
	byTx := map[string][]Hit{}
	for _, h := range win {
		tx := h.Point.Tag.Tx
		if _, ok := byTx[tx]; !ok {
			order = append(order, tx)
		}
		byTx[tx] = append(byTx[tx], h)
	}
	var out []Target
	for _, tx := range order {
		if p, ok := beforeLastFile(byTx[tx]); ok {
			out = append(out, Target{Gen: 1, Point: p})
		}
	}
	return out
}

// beforeLastFile finds, among the hits of one transaction, the first run of it
// through its last visible step. A step makes a file visible when it renames, makes
// a directory or removes; a write makes an appended file longer and is such a step
// for a role that has no temporary file (no create step). With a commit in the
// window the point is the last visible step before the first commit; without one,
// the first visible step of the role that appears last.
func beforeLastFile(hits []Hit) (fsx.Point, bool) {
	temp := map[string]bool{} // roles that write through a temporary file
	for _, h := range hits {
		if h.Point.Step == fsx.StepCreate {
			temp[h.Point.Tag.Role] = true
		}
	}
	visible := func(h Hit) bool {
		switch h.Point.Step {
		case fsx.StepRename, fsx.StepRenameDir, fsx.StepRemove, fsx.StepMkdir, fsx.StepRemoveAll:
			return true
		case fsx.StepWrite:
			return !temp[h.Point.Tag.Role]
		}
		return false
	}
	var last *Hit
	for i, h := range hits {
		if h.Point.Step == fsx.StepCommit {
			if last != nil {
				return last.Point, true
			}
			break
		}
		if visible(h) {
			last = &hits[i]
		}
	}
	// No commit in the window: the first visible step of the role seen last.
	var roleOrder []string
	firstOf := map[string]Hit{}
	for _, h := range hits {
		if !visible(h) {
			continue
		}
		role := h.Point.Tag.Role
		if _, ok := firstOf[role]; !ok {
			roleOrder = append(roleOrder, role)
			firstOf[role] = h
		}
	}
	if len(roleOrder) == 0 {
		return fsx.Point{}, false
	}
	return firstOf[roleOrder[len(roleOrder)-1]].Point, true
}

// PlanRecovery returns the recovery cases a walk runs: all of them in Full, the
// first listed of each recovery transaction in Smoke, in both modes ordered by the
// write they name. The cases of a scenario are a static list, so the choice does
// not depend on a recording; the order is fixed anyway so that the numbering of a
// shard does not depend on how a scenario happens to list its cases.
func PlanRecovery(cases []RecoveryCase, mode Mode) []RecoveryCase {
	var out []RecoveryCase
	if mode == Full {
		out = append(out, cases...)
	} else {
		seen := map[string]bool{}
		for _, c := range cases {
			if tx := c.Item.Tag.Tx; !seen[tx] {
				seen[tx] = true
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Item.String() < out[j].Item.String() })
	return out
}
