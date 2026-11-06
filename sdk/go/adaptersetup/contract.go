// Package adaptersetup is the contract between the core CLI and an adapter's
// client setup, and the code every adapter's setup shares.
//
// An adapter that has to bring its client's own configuration in line with an
// instance (register the MCP server, allow its tools, install a Skill) declares a
// setup program in its manifest. `sidera adapter setup` runs that program with one
// Request as JSON on its standard input and reads one Result as JSON from its
// standard output. The adapter never talks to the terminal: every question is the
// core CLI's, asked from the plan the adapter returned. The contract and the reasons
// for it are in the adapter-setup reference.
package adaptersetup

import (
	"encoding/json"
	"fmt"
)

// Version is the newest major version of the contract this package speaks: the
// version the core CLI writes into its requests and demands in the results it reads.
// Both request and result carry it; a side that does not support the version it is
// given fails with a message that says so.
//
// Members are added without changing it: both sides ignore a member they do not
// know. So are the values of Action and Problem, which the core CLI shows and acts
// on nothing but the presence of. A change in the meaning of a member, a new value of
// Status or Op, or an outcome status that an older program would read as a failure
// when it is not one, changes the version.
const Version = 1

// MinVersion is the oldest version an adapter's setup program still serves. During
// `sidera update` the old core program runs the new adapter, so a program that takes
// the versions MinVersion to Version, answers in the version it was asked (the Version
// of its result is the request's) and tells its Handler which that is (Request.Version),
// can move to a new Version while the core programs it has to serve still speak the old
// one.
const MinVersion = 1

// Op is what a request asks the adapter to do.
type Op string

const (
	// OpPlan lists what setup would do for the instance and changes nothing.
	OpPlan Op = "plan"
	// OpApply does the items of the plan that Request.Accepted names; with
	// Request.Undo it undoes the items of a removal that it names.
	OpApply Op = "apply"
	// OpRefresh brings the recorded items that still exist up to date, without
	// asking, and reports what it would have had to create or what is gone.
	OpRefresh Op = "refresh"
	// OpCheck reports whether a record exists and what has drifted from it.
	OpCheck Op = "check"
	// OpRemove lists the recorded items that setup would undo, as OpPlan lists
	// what it would do; the items are then undone by OpApply with Request.Undo.
	OpRemove Op = "remove"
)

func (o Op) valid() bool {
	switch o {
	case OpPlan, OpApply, OpRefresh, OpCheck, OpRemove:
		return true
	}
	return false
}

// Request is what the core CLI writes to the standard input of the setup program.
type Request struct {
	Version int `json:"version"`
	Op      Op  `json:"op"`

	// Home is the absolute path of the data directory the command resolved, and
	// HomeIsDefault says whether it is where a node of this user is by default (no
	// --home, no SIDERA_HOME): a client's registration of `sidera mcp` names the
	// directory only when it is not the default.
	Home          string `json:"home"`
	HomeIsDefault bool   `json:"home_is_default"`

	// Instance and StateDir are the instance and the directory its adapter keeps its
	// state in. The directory exists when the instance has run once; setup never
	// creates it (see RequireStateDir).
	Instance string `json:"instance"`
	StateDir string `json:"state_dir"`

	// Config is what the owner set for the instance: the same object adapter.start
	// passes, with no default applied. It may hold a secret; it is never logged.
	Config map[string]json.RawMessage `json:"config"`

	// SideraProgram is the absolute path of the core program as it was launched, links
	// kept: the file `sidera update` replaces in place, so a client's configuration
	// stays valid across an update and a package manager's link keeps pointing at the
	// current version. Only when the launch path is not known is it what the platform
	// reports for the running executable, made absolute. It is never quoted: it goes into
	// a client's configuration as data, and an adapter that prints it for a shell quotes
	// it itself.
	SideraProgram string `json:"sidera_program"`

	// Options are the adapter's own arguments: the words after `--` on the command
	// line, in order and untouched.
	Options []string `json:"options"`

	// Accepted are the ids of the items to do, for OpApply. The core CLI always
	// includes the items that need no consent.
	Accepted []string `json:"accepted"`

	// Declined, with OpApply, are the ids of the items that follow from a no: the
	// items of the plan that need consent that the person was asked about and answered
	// no to, and the ones left alone because an item they require was declined (they
	// were never asked, but the person decided against the chain). Not the items left
	// alone because a required item failed or was skipped when it was applied: that is
	// not a decision, and they are reported until setup is run again. Handle remembers
	// them in the record, so that a check and a refresh stop reporting them as needing
	// setup; a plan still lists them. An older core program never sends it, and then nothing
	// is remembered; an adapter built before it was added ignores it.
	Declined []string `json:"declined,omitempty"`

	// Undo, with OpApply, says that Accepted are ids of an OpRemove listing and the
	// items are to be undone. Ids of OpPlan and of OpRemove are not told apart by
	// their spelling, so the request says which it carries.
	Undo bool `json:"undo,omitempty"`
}

// Action is what an item of a plan does to its target.
type Action string

const (
	// ActionCreate makes something that is not there.
	ActionCreate Action = "create"
	// ActionUpdate changes something setup recorded.
	ActionUpdate Action = "update"
	// ActionRemove takes something away. OpRemove lists it for the items setup
	// recorded; a plan lists it for something setup did not make that stands in the way
	// of what it wants (an earlier installation of the same thing, say), and such an
	// item always needs consent.
	ActionRemove Action = "remove"
	// ActionAdopt takes something that is already there, and that setup did not
	// make, into its record, so that refresh and remove then keep it.
	ActionAdopt Action = "adopt"
)

// Item is one thing a plan would do, or a removal would undo.
type Item struct {
	// ID names the item within one result: unique, and stable between the plan and
	// the apply that follows it.
	ID string `json:"id"`
	// Kind says what sort of thing it is, in the adapter's words (for example
	// "mcp_registration", "permission", "plugin", "file"); the core CLI shows it.
	Kind string `json:"kind"`
	// Target says where it lands: a path, or the name of what is registered.
	Target string `json:"target"`
	Action Action `json:"action"`
	// Summary is one phrase, written as an instruction ("register the sidera MCP
	// server with Claude Code"); the core CLI shows it and, for an item that needs
	// consent, asks it as a question.
	Summary string `json:"summary"`
	// Consent says whether the core CLI asks before the item is done. An item that
	// needs none is one the adapter would do on its own authority: a file inside its
	// own state directory, which nothing but the adapter reads. Everything that
	// touches the client's configuration, adopts something that was there, or
	// removes anything the user could have changed needs consent.
	Consent bool `json:"consent"`
	// Requires lists ids of earlier items of the same listing that this one cannot be
	// done without: the source of a plugin cannot be deleted while the plugin stays
	// installed. The core CLI does not ask about an item whose required item was
	// declined, does not apply it when the required item did not complete, and says
	// so. Naming only earlier items keeps the order of a listing a valid order of doing.
	Requires []string `json:"requires,omitempty"`
}

// OutcomeStatus says what became of one item.
type OutcomeStatus string

const (
	// OutcomeDone: the item was done (created, updated, adopted, removed or
	// refreshed).
	OutcomeDone OutcomeStatus = "done"
	// OutcomeSkipped: the item was not done, and that is not a failure: it was not
	// asked for, no longer applies, or depends on one that was not accepted. The
	// message says which.
	OutcomeSkipped OutcomeStatus = "skipped"
	// OutcomeFailed: the item was tried and could not be done; the message says why.
	OutcomeFailed OutcomeStatus = "failed"
)

// Outcome is what became of one item of an apply, an undo or a refresh.
type Outcome struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	Target  string        `json:"target"`
	Status  OutcomeStatus `json:"status"`
	Message string        `json:"message,omitempty"`
}

// Failed says whether the item counts as a failure: anything but done and skipped. A
// status that a later release adds is therefore read as a failure, with the adapter's
// message, instead of making the whole result unusable.
func (o Outcome) Failed() bool {
	return o.Status != OutcomeDone && o.Status != OutcomeSkipped
}

// Problem says how a recorded item, or one setup now wants, differs from what is
// there. The core CLI shows it and acts on nothing but whether there is drift, so a
// release can add a value: an older program shows it as it is.
type Problem string

const (
	// ProblemMissing: a recorded item is gone (the user removed it, or the source
	// directory vanished). Refresh never makes it again.
	ProblemMissing Problem = "missing"
	// ProblemChanged: a recorded item's content is not what was recorded (someone
	// edited it). Refresh leaves it alone.
	ProblemChanged Problem = "changed"
	// ProblemStale: a recorded item is what was recorded but not what this version
	// of the adapter writes; refresh brings it up to date.
	ProblemStale Problem = "stale"
	// ProblemBroken: the item's target cannot work as it is (a configuration file
	// that is not valid, a plugin the user disabled).
	ProblemBroken Problem = "broken"
	// ProblemNeedsSetup: this version of the adapter wants an item that no record
	// has. Only `adapter setup` makes it, after asking.
	ProblemNeedsSetup Problem = "needs_setup"
)

// Drift is one difference found by a check or a refresh.
type Drift struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Target  string  `json:"target"`
	Problem Problem `json:"problem"`
	// Summary says what is wrong and what puts it right, for a person.
	Summary string `json:"summary"`
}

// Status is the verdict of a result as a whole. The helper that writes a result
// derives it from the rest, so that an adapter cannot report one that contradicts
// its lists.
type Status string

const (
	// StatusOK: a plan or a removal was listed; an apply or an undo did every item
	// it was given without a failure; a check found a record and no drift.
	StatusOK Status = "ok"
	// StatusRefreshed: a refresh found a record and nothing left to do; the outcomes
	// list what it rewrote, which may be nothing.
	StatusRefreshed Status = "refreshed"
	// StatusNone: a check or a refresh found no record: setup has not run, or was
	// undone.
	StatusNone Status = "none"
	// StatusDrift: a check or a refresh found a difference that needs a person.
	StatusDrift Status = "drift"
	// StatusError: an apply, an undo or a refresh had an item fail.
	StatusError Status = "error"
)

func (s Status) valid() bool {
	switch s {
	case StatusOK, StatusRefreshed, StatusNone, StatusDrift, StatusError:
		return true
	}
	return false
}

// allowedFor says whether an op can end in the status: the statuses Handle derives for it.
// A refresh is never "ok" (its quiet outcome is "refreshed"), and a check never "error"
// or "refreshed".
func (s Status) allowedFor(op Op) bool {
	switch op {
	case OpPlan, OpRemove:
		return s == StatusOK
	case OpApply:
		return s == StatusOK || s == StatusError
	case OpCheck:
		return s == StatusOK || s == StatusNone || s == StatusDrift
	case OpRefresh:
		return s == StatusRefreshed || s == StatusNone || s == StatusDrift || s == StatusError
	}
	return false
}

// Result is what the setup program writes to its standard output.
type Result struct {
	Version int    `json:"version"`
	Op      Op     `json:"op"`
	Status  Status `json:"status"`
	// Recorded says whether a record exists. Check and refresh set it; the other
	// ops leave it out.
	Recorded *bool `json:"recorded,omitempty"`
	// Items are what OpPlan and OpRemove list.
	Items []Item `json:"items,omitempty"`
	// Outcomes are what OpApply and OpRefresh did, item by item.
	Outcomes []Outcome `json:"outcomes,omitempty"`
	// Drift is what OpCheck and OpRefresh found.
	Drift []Drift `json:"drift,omitempty"`
	// Notes are what OpPlan and OpCheck tell the person that is not drift and not an
	// item: advice about something setup does not install, such as which toolsets of a
	// client to enable by hand. The core CLI shows them and they never change the status
	// or the exit code, so a standing piece of advice does not make a check fail for
	// good. A release may let more ops carry notes, and Validate does not refuse them on
	// any op, so an older core program mid-update is not broken by it; it shows the notes
	// of a plan and a check only.
	Notes []string `json:"notes,omitempty"`
}

// Validate says whether the result is one the core CLI can act on: it carries the version
// of the request it answers (version) and the op that was asked (op), the status is one of
// this version and one the op can have (it decides the exit code, so a guess is not made),
// the ids of the items are unique, an item requires only earlier ones, check and refresh say
// whether a record exists, and a plan asks before it removes. A value of Action, Problem or an outcome's status that
// a later release added is not an error (see Version and Outcome.Failed), and neither is a
// member this version does not know.
func (r Result) Validate(op Op, version int) error {
	if r.Version != version {
		return fmt.Errorf("the result is of version %d, not the version %d that was asked", r.Version, version)
	}
	if r.Op != op {
		return fmt.Errorf("the result is for %q, not the %q that was asked", r.Op, op)
	}
	if !r.Status.valid() {
		return fmt.Errorf("the status %q is not one of this version", r.Status)
	}
	if !r.Status.allowedFor(op) {
		return fmt.Errorf("the status %q is not one a %s can have", r.Status, op)
	}
	if (op == OpCheck || op == OpRefresh) && r.Recorded == nil {
		return fmt.Errorf("the result of %s does not say whether a record exists (recorded)", op)
	}
	position := map[string]int{}
	for i, it := range r.Items {
		switch {
		case it.ID == "":
			return fmt.Errorf("items[%d] has no id", i)
		case it.Action == "":
			return fmt.Errorf("items[%d] has no action", i)
		case op == OpPlan && it.Action == ActionRemove && !it.Consent:
			return fmt.Errorf("the plan removes %q without asking (consent): only the removal of what setup recorded may need none", it.ID)
		}
		if _, dup := position[it.ID]; dup {
			return fmt.Errorf("the item id %q is used twice", it.ID)
		}
		for _, req := range it.Requires {
			if _, earlier := position[req]; !earlier {
				return fmt.Errorf("the item %q requires %q, which is not an item before it", it.ID, req)
			}
		}
		position[it.ID] = i
	}
	for i, o := range r.Outcomes {
		if o.ID == "" {
			return fmt.Errorf("outcomes[%d] has no id", i)
		}
	}
	for i, d := range r.Drift {
		switch {
		case d.ID == "":
			return fmt.Errorf("drift[%d] has no id", i)
		case d.Problem == "":
			return fmt.Errorf("drift[%d] has no problem", i)
		}
	}
	return nil
}
