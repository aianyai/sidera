package adaptersetup

// Wanted is an item that this version of an adapter wants setup to have made.
type Wanted struct {
	ID     string
	Kind   string
	Target string
	// Digest is the Digest of the content this version writes for the item.
	Digest string
}

// Probe reports the Digest of what a recorded item's target holds now, and whether
// the target exists at all. An error is a failure to look, not a difference.
type Probe func(RecordItem) (digest string, exists bool, err error)

// Reconciliation is what comparing the record with what an adapter wants found.
type Reconciliation struct {
	// Stale are the wanted items whose recorded content is intact but is not what
	// this version writes: the ones a refresh rewrites without asking.
	Stale []Wanted
	// Drift is what needs a person: a recorded item that is gone (ProblemMissing) or
	// was edited (ProblemChanged), and a wanted item that no record has
	// (ProblemNeedsSetup). A refresh leaves all of it as it is.
	Drift []Drift
}

// CheckDrift is the drift a check reports: Drift, and the stale items as ProblemStale.
func (r Reconciliation) CheckDrift() []Drift {
	out := append([]Drift(nil), r.Drift...)
	for _, w := range r.Stale {
		out = append(out, Drift{ID: w.ID, Kind: w.Kind, Target: w.Target, Problem: ProblemStale,
			Summary: w.ID + " is older than what this version writes; refresh brings it up to date"})
	}
	return out
}

// Reconcile classifies each wanted item against the record, in the order given. It
// looks only at what the record holds (a nil record holds nothing): an item that is
// recorded is probed at its recorded target and found intact, stale, changed or gone,
// and an item that is not recorded needs setup. It changes nothing; the classification
// is what every adapter's check and refresh share, so that a refresh can only ever
// rewrite what a check calls stale.
func Reconcile(rec *Record, wanted []Wanted, probe Probe) (Reconciliation, error) {
	var out Reconciliation
	for _, w := range wanted {
		drift := Drift{ID: w.ID, Kind: w.Kind, Target: w.Target}
		var recorded RecordItem
		var isRecorded bool
		if rec != nil {
			recorded, isRecorded = rec.Find(w.ID)
		}
		if !isRecorded {
			drift.Problem, drift.Summary = ProblemNeedsSetup, w.ID+" has no record; run setup"
			out.Drift = append(out.Drift, drift)
			continue
		}
		digest, exists, err := probe(recorded)
		switch {
		case err != nil:
			return Reconciliation{}, err
		case !exists:
			drift.Problem, drift.Summary = ProblemMissing, w.ID+" is gone; setup does not make it again, run setup to do so"
			out.Drift = append(out.Drift, drift)
		case digest != recorded.Digest:
			drift.Problem, drift.Summary = ProblemChanged, w.ID+" was changed since setup wrote it; setup leaves it as it is"
			out.Drift = append(out.Drift, drift)
		case recorded.Digest != w.Digest:
			out.Stale = append(out.Stale, w)
		}
	}
	return out, nil
}
