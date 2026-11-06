package adaptersetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"slices"

	"github.com/aianyai/sidera/sdk/go/adapterkit"
)

// maxRequest bounds what the helper reads from standard input: a request holds an
// instance's configuration and a few paths.
const maxRequest = 4 << 20

// Handler is an adapter's setup. The helper decodes the request, checks its
// version, calls the method of its op and writes the result; the adapter writes
// only what it does.
//
// Every method gets the whole Request, whose Version is the one the core program
// speaks (between MinVersion and Version). Handle refuses an instance whose state
// directory does not exist (RequireStateDir) before it calls Plan, Apply, Remove or
// Undo, so a handler need not. An error is the adapter's failure to do the op at all:
// it is written on standard error and the program exits 1. A failure of one item is an
// Outcome, not an error.
type Handler interface {
	// Plan lists what setup would do and changes nothing.
	Plan(ctx context.Context, req Request) ([]Item, error)
	// Apply does the items req.Accepted names, which came from Plan, and returns an
	// outcome for each. An id that Plan would not list now is skipped, not an error.
	Apply(ctx context.Context, req Request) ([]Outcome, error)
	// Remove lists the recorded items that setup would undo, each with the action
	// ActionRemove, and changes nothing. An instance with no record has none.
	Remove(ctx context.Context, req Request) ([]Item, error)
	// Undo undoes the items req.Accepted names, which came from Remove, and returns
	// an outcome for each.
	Undo(ctx context.Context, req Request) ([]Outcome, error)
	// Refresh brings the recorded items that still exist up to date, without asking.
	Refresh(ctx context.Context, req Request) (Refresh, error)
	// Check reports whether a record exists and what drifted; it changes nothing. It
	// returns an error only when it cannot read the record. A client it cannot probe
	// (not installed, not answering) is drift of the problem ProblemBroken, not an
	// error: `sidera adapter remove` runs Check, and an instance whose client is gone
	// must still be removable. Without a record it still reports what setup would have
	// to make (ProblemNeedsSetup), so that --check says an instance has no setup yet.
	Check(ctx context.Context, req Request) (Check, error)
}

// Noter is what a Handler also implements when it has advice for the person that is not
// something setup installs, so not an item of a plan and not drift of a check: which
// toolsets of the client to enable, say. Handle asks it for OpPlan and OpCheck only,
// after the op itself, and puts the notes in the result. The notes change nothing about
// the status. Notes follows Check's rule: it returns an error only when it cannot read the
// instance's own state. A client it cannot probe gives no note, or a note that says so,
// because `sidera adapter remove` runs Check, and an error from Notes would make an
// instance whose client is gone unremovable without --force.
type Noter interface {
	Notes(ctx context.Context, req Request) ([]string, error)
}

// Check is the answer of Handler.Check.
type Check struct {
	// Recorded is ignored: Handle takes recorded from the record itself
	// (Record.Installed), so that an adapter's slip cannot make a record of declines
	// alone block `adapter remove`.
	Recorded bool
	Drift    []Drift
}

// Refresh is the answer of Handler.Refresh. Without a record there is nothing to
// refresh and nothing to report: Handle refuses outcomes and drift then, and an
// outcome of the status done for an item the record does not hold.
type Refresh struct {
	// Recorded is ignored as Check.Recorded is: Handle takes it from the record.
	Recorded bool
	// Outcomes are the items it rewrote (or tried to and could not, or left on
	// purpose); an item that was already current is not listed.
	Outcomes []Outcome
	// Drift is what it did not do: a recorded item that is gone or changed, and an
	// item the adapter now wants that no record has.
	Drift []Drift
}

// Command is the `setup` command of an adapter program, for adapterkit.Program's
// Commands. program names the program in its messages. It takes no argument but
// -h and --help, reads one request from standard input and writes one result to
// standard output; the exit codes are the program table's: 0 when a result was
// written, 2 for a request it cannot take (it is not JSON, its version is not
// supported, its op is not one) and 1 when the handler fails. An interrupt or a
// termination signal (and a hangup, where there is one) cancels the context the
// handler runs under, so that the client it is running is killed with it.
func Command(program string, h Handler) adapterkit.Command {
	return func(args []string, s adapterkit.Streams) int {
		if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
			fmt.Fprintf(s.Stdout, "usage: %s setup\nReads one setup request as JSON on standard input and writes one result as JSON on standard output; it is run by `sidera adapter setup`, not by a person.\n", program)
			return adapterkit.ExitOK
		}
		if len(args) != 0 {
			fmt.Fprintf(s.Stderr, "%s: setup takes no argument; it reads a request on standard input\n", program)
			return adapterkit.ExitUsage
		}
		req, err := ReadRequest(s.Stdin)
		if err != nil {
			fmt.Fprintf(s.Stderr, "%s: setup: %v\n", program, err)
			return adapterkit.ExitUsage
		}
		// The client a handler runs is in a process group of its own and does not receive
		// the terminal's Ctrl-C, so this program turns the signals that end it into the end
		// of the handler's context, which ClientCommand.Run answers by killing the client.
		ctx, stop := signal.NotifyContext(context.Background(), interruptSignals...)
		defer stop()
		res, err := Handle(ctx, h, req)
		if err != nil {
			fmt.Fprintf(s.Stderr, "%s: setup %s: %v\n", program, req.Op, err)
			return adapterkit.ExitFailed
		}
		enc := json.NewEncoder(s.Stdout)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(res); err != nil {
			fmt.Fprintf(s.Stderr, "%s: setup %s: %v\n", program, req.Op, err)
			return adapterkit.ExitFailed
		}
		return adapterkit.ExitOK
	}
}

// ReadRequest reads one request from r and checks that this version of the package
// can take it: a version from MinVersion to Version. A member it does not know is
// ignored.
func ReadRequest(r io.Reader) (Request, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxRequest+1))
	if err != nil {
		return Request{}, fmt.Errorf("cannot read the request: %w", err)
	}
	if len(data) > maxRequest {
		return Request{}, errors.New("the request is too large")
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{}, fmt.Errorf("the request is not a JSON object: %w", err)
	}
	if req.Version < MinVersion || req.Version > Version {
		supported := fmt.Sprintf("version %d", Version)
		if MinVersion != Version {
			supported = fmt.Sprintf("versions %d to %d", MinVersion, Version)
		}
		return Request{}, fmt.Errorf("the request is of version %d, and this adapter supports %s: update sidera and its adapters together", req.Version, supported)
	}
	if !req.Op.valid() {
		return Request{}, fmt.Errorf("the request asks for %q, which is not an op of version %d", req.Op, Version)
	}
	return req, nil
}

// Handle calls the method of the request's op and builds the result, in the version
// of the request and with a status that follows from its lists. What an adapter
// must not do is enforced here once, for every adapter: it does not touch an instance
// without a state directory, and a refresh does not create anything or speak of a
// record that is not there. Two things are the record's to say and are taken from it
// whatever the adapter answered: whether a check or a refresh finds a record (recorded,
// which `adapter remove` acts on) and, after an apply, the set of declined ids. A result
// that breaks such a rule, or is not valid, is the adapter's bug and an error.
func Handle(ctx context.Context, h Handler, req Request) (Result, error) {
	res := Result{Version: req.Version, Op: req.Op}
	if req.Op != OpCheck && req.Op != OpRefresh {
		if err := RequireStateDir(req.StateDir); err != nil {
			return Result{}, err
		}
	}
	switch req.Op {
	case OpPlan:
		items, err := h.Plan(ctx, req)
		if err != nil {
			return Result{}, err
		}
		res.Status, res.Items = StatusOK, items
	case OpRemove:
		items, err := h.Remove(ctx, req)
		if err != nil {
			return Result{}, err
		}
		res.Status, res.Items = StatusOK, items
	case OpApply:
		run := h.Apply
		if req.Undo {
			run = h.Undo
		}
		// The record as it was before the adapter touched it: the declines of earlier
		// runs are read from here, so that an adapter that writes its record from
		// scratch cannot drop them.
		before, err := ReadRecord(req.StateDir)
		if err != nil {
			return Result{}, err
		}
		outcomes, err := run(ctx, req)
		if err != nil {
			return Result{}, err
		}
		if req.Undo {
			err = dropEmptyRecord(req.StateDir)
		} else {
			err = rememberDeclined(req, before)
		}
		if err != nil {
			return Result{}, err
		}
		res.Status, res.Outcomes = StatusOK, outcomes
		if anyFailed(outcomes) {
			res.Status = StatusError
		}
	case OpCheck:
		c, err := h.Check(ctx, req)
		if err != nil {
			return Result{}, err
		}
		rec, err := ReadRecord(req.StateDir)
		if err != nil {
			return Result{}, err
		}
		recorded := rec.Installed()
		res.Recorded, res.Drift = &recorded, c.Drift
		res.Status = StatusOK
		switch {
		case len(c.Drift) > 0:
			res.Status = StatusDrift
		case !recorded:
			res.Status = StatusNone
		}
	case OpRefresh:
		rec, err := ReadRecord(req.StateDir)
		if err != nil {
			return Result{}, err
		}
		f, err := h.Refresh(ctx, req)
		if err != nil {
			return Result{}, err
		}
		if err := checkRefresh(rec, f); err != nil {
			return Result{}, err
		}
		recorded := rec.Installed()
		res.Recorded, res.Outcomes, res.Drift = &recorded, f.Outcomes, f.Drift
		res.Status = StatusRefreshed
		switch {
		case anyFailed(f.Outcomes):
			res.Status = StatusError
		case len(f.Drift) > 0:
			res.Status = StatusDrift
		case !recorded:
			res.Status = StatusNone
		}
	}
	if noter, ok := h.(Noter); ok && (req.Op == OpPlan || req.Op == OpCheck) {
		notes, err := noter.Notes(ctx, req)
		if err != nil {
			return Result{}, err
		}
		res.Notes = notes
	}
	if err := res.Validate(req.Op, req.Version); err != nil {
		return Result{}, fmt.Errorf("the adapter built an invalid result: %w", err)
	}
	return res, nil
}

// checkRefresh holds a refresh to the record as it was before the refresh: with no
// record (or one that holds only declines) there is nothing to report, and nothing the record does not hold is done. A
// refresh that made an item would undo a user's deletion, which the reference promises
// never happens; the check is here so that no adapter has to remember it.
func checkRefresh(before *Record, f Refresh) error {
	if !before.Installed() {
		if len(f.Outcomes) > 0 || len(f.Drift) > 0 {
			return errors.New("the adapter's refresh reported outcomes or drift for an instance with no record; with no record the answer is none")
		}
		return nil
	}
	for _, o := range f.Outcomes {
		if o.Status != OutcomeDone {
			continue
		}
		if _, recorded := before.Find(o.ID); !recorded {
			return fmt.Errorf("the adapter's refresh did the item %q, which the record does not hold: a refresh never creates", o.ID)
		}
	}
	return nil
}

func anyFailed(outcomes []Outcome) bool {
	for _, o := range outcomes {
		if o.Failed() {
			return true
		}
	}
	return false
}

// rememberDeclined keeps in the record what the person declined in an apply, so that
// the declines have one writer for every adapter. It runs after the adapter's Apply,
// which has written whatever it did to Items. The declined set starts from the record as
// it was before the Apply (before, nil for none), not from what the adapter wrote back,
// which may have forgotten it: it is that set, plus req.Declined, minus the items
// accepted now (the person wants them, whether or not they worked) and minus the items
// the record holds now. A record is made for declines alone, since a person who declines
// everything leaves no other trace; and nothing is written when the set does not change.
func rememberDeclined(req Request, before *Record) error {
	rec, err := ReadRecord(req.StateDir)
	if err != nil {
		return err
	}
	if rec == nil {
		rec = &Record{SideraProgram: req.SideraProgram}
	}
	var declined []string
	if before != nil {
		declined = slices.Clone(before.Declined)
	}
	for _, id := range req.Declined {
		if !slices.Contains(declined, id) {
			declined = append(declined, id)
		}
	}
	declined = slices.DeleteFunc(declined, func(id string) bool {
		_, recorded := rec.Find(id)
		return recorded || slices.Contains(req.Accepted, id)
	})
	if slices.Equal(declined, rec.Declined) {
		return nil
	}
	rec.Declined = declined
	return WriteRecord(req.StateDir, *rec)
}

// dropEmptyRecord removes a record that holds no item after an undo, declines
// included: with nothing left that setup did, the setup is undone, and a later setup
// asks about everything again.
func dropEmptyRecord(stateDir string) error {
	rec, err := ReadRecord(stateDir)
	if err != nil || rec == nil || rec.Installed() {
		return err
	}
	return RemoveRecord(stateDir)
}
