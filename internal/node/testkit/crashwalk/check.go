package crashwalk

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
)

// CheckBudget fails when the window of a recording has more main crash points than
// the scenario declared. The failure puts the growth in front of a reviewer, who
// must look at the time a full walk now takes and raise the budget on purpose;
// sampling the points instead would leave some of them untested without a trace.
func CheckBudget(s Scenario, rec []Hit) error {
	n := len(Plan(rec, Full, nil))
	if n > s.MaxPoints() {
		return fmt.Errorf("crashwalk: scenario %q has %d crash points in its window, over its budget of %d: it writes more than it did when the budget was set; check how long a full walk takes now and raise the budget", s.Name(), n, s.MaxPoints())
	}
	return nil
}

// Divergence says where a rerun stopped agreeing with the recording.
type Divergence struct {
	Target Target
	// Tag is the transaction and role whose writes differ.
	Tag fsx.Tag
	// Index is the position in that tag's writes, from 0; Want and Got are what
	// the recording and the rerun had there (empty when one ran out).
	Index     int
	Want, Got string
	Reason    string
}

func (d *Divergence) Error() string {
	return fmt.Sprintf("crashwalk: the rerun to %v is not deterministic: %s/%s write %d: %s (recorded %q, rerun %q)",
		d.Target.Point, d.Tag.Tx, d.Tag.Role, d.Index, d.Reason, d.Want, d.Got)
}

func describe(h Hit) string {
	description := h.Point.String() + " " + h.Path
	if h.From != "" {
		description += " from " + h.From
	}
	return description
}

// CompareRuns checks a rerun against the recording, up to the target. Each pair of
// transaction and role must have written the same steps, in the same order, with
// the same path operands, including both ends of a rename. The order across pairs
// is not compared: writes outside the loop may interleave with writes inside it,
// which changes no pair's own order and no file
// a point names. The rerun must also have ended at the target, the point it was
// to be killed at.
func CompareRuns(recorded, rerun []Hit, upTo Target) error {
	if len(rerun) == 0 || rerun[len(rerun)-1].Point != upTo.Point {
		got := ""
		if len(rerun) > 0 {
			got = describe(rerun[len(rerun)-1])
		}
		return &Divergence{Target: upTo, Tag: upTo.Point.Tag, Index: -1, Want: upTo.Point.String(), Got: got, Reason: "the run did not end at its target"}
	}
	// The recorded prefix: everything before the target in the recording.
	var prefix []Hit
	found := false
	for _, h := range recorded {
		if h.Gen != 1 {
			continue
		}
		prefix = append(prefix, h)
		if h.Point == upTo.Point {
			found = true
			break
		}
	}
	if !found {
		return &Divergence{Target: upTo, Tag: upTo.Point.Tag, Index: -1, Want: upTo.Point.String(), Reason: "the recording never reached the target"}
	}
	want, got := byTag(prefix), byTag(rerun)
	tags := make([]fsx.Tag, 0, len(want))
	for tag := range want {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Tx != tags[j].Tx {
			return tags[i].Tx < tags[j].Tx
		}
		return tags[i].Role < tags[j].Role
	})
	for _, tag := range tags {
		w, g := want[tag], got[tag]
		n := min(len(w), len(g))
		if tag == upTo.Point.Tag {
			// The rerun was killed at the target: its own pair has exactly the
			// writes the recording made up to it.
			n = len(w)
			if len(g) != len(w) {
				return &Divergence{Target: upTo, Tag: tag, Index: min(len(w), len(g)), Reason: "another number of writes before the target",
					Want: fmt.Sprint(len(w)), Got: fmt.Sprint(len(g))}
			}
		}
		for i := 0; i < n; i++ {
			if w[i].Point != g[i].Point || w[i].Path != g[i].Path || w[i].From != g[i].From {
				return &Divergence{Target: upTo, Tag: tag, Index: i, Want: describe(w[i]), Got: describe(g[i]), Reason: "another step or another path operand"}
			}
		}
	}
	return nil
}

func byTag(hits []Hit) map[fsx.Tag][]Hit {
	out := map[fsx.Tag][]Hit{}
	for _, h := range hits {
		out[h.Point.Tag] = append(out[h.Point.Tag], h)
	}
	return out
}

// Coverage compares the hits of a set of runs with the declared crash points.
type Coverage struct {
	// MissingNormal are declared keys of normal transactions that no hit reached.
	MissingNormal []fsx.Key
	// MissingRecovery are declared keys of recovery transactions that no hit
	// reached.
	MissingRecovery []fsx.Key
	// Undeclared are keys that were hit and that no declaration lists: a write that
	// does not go through a declared transaction.
	Undeclared []fsx.Key
	// Normal and Recovery count the declared writes of each kind and how many of
	// them were reached; the two kinds are reported apart because they are
	// reached by different runs.
	Normal, Recovery PathCount
}

// PathCount is how many writes of a kind are declared and how many were reached.
type PathCount struct {
	Declared, Reached int
}

// Assess compares hits with the declared keys of the table. A sync step counts as
// hit when it was reached, even though the walk never kills there.
func Assess(table *txn.Table, hits []Hit) Coverage {
	declared := map[fsx.Key]txn.Kind{}
	for _, k := range table.Keys() {
		declared[k.Key] = k.Kind
	}
	reached := map[fsx.Key]bool{}
	undeclared := map[fsx.Key]bool{}
	for _, h := range hits {
		if _, ok := declared[h.Point.Key]; ok {
			reached[h.Point.Key] = true
		} else {
			undeclared[h.Point.Key] = true
		}
	}
	var c Coverage
	for _, k := range table.Keys() {
		count, missing := &c.Normal, &c.MissingNormal
		if k.Kind == txn.Recovery {
			count, missing = &c.Recovery, &c.MissingRecovery
		}
		count.Declared++
		if reached[k.Key] {
			count.Reached++
		} else {
			*missing = append(*missing, k.Key)
		}
	}
	for k := range undeclared {
		c.Undeclared = append(c.Undeclared, k)
	}
	sort.Slice(c.Undeclared, func(i, j int) bool { return c.Undeclared[i].String() < c.Undeclared[j].String() })
	return c
}

// CheckRecoveryCases is the static check that every recovery write of the table is
// the responsibility of exactly one case: the cases of all scenarios together are
// the recovery keys that are not sync steps, each once, and nothing else. It runs
// without running any scenario, so a missing case shows when a recovery write is
// declared and not on the day a walk happens to be run in full.
func CheckRecoveryCases(table *txn.Table, scenarios []Scenario) error {
	return checkRecoveryCases(table, scenarios, nil)
}

// A nil required set retains the public complete-table contract.
func checkRecoveryCases(table *txn.Table, scenarios []Scenario, required map[fsx.Key]bool) error {
	want := map[fsx.Key]bool{}
	for _, k := range table.Keys() {
		if k.Kind == txn.Recovery && !IsSync(k.Step) {
			want[k.Key] = true
		}
	}
	got := map[fsx.Key]string{}
	var problems []string
	for _, s := range scenarios {
		for _, c := range s.Recovery() {
			switch {
			case !want[c.Item]:
				problems = append(problems, fmt.Sprintf("scenario %q lists %v, which is not a recovery write or is a sync step", s.Name(), c.Item))
			case required != nil && !required[c.Item]:
				problems = append(problems, fmt.Sprintf("scenario %q assigns CoveredElsewhere recovery write %v", s.Name(), c.Item))
			case got[c.Item] != "":
				problems = append(problems, fmt.Sprintf("%v is listed by %q and by %q", c.Item, got[c.Item], s.Name()))
			default:
				got[c.Item] = s.Name()
			}
		}
	}
	var missing []string
	for k := range want {
		if (required == nil || required[k]) && got[k] == "" {
			missing = append(missing, k.String())
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		problems = append(problems, "no scenario crashes these recovery writes: "+strings.Join(missing, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("crashwalk: " + strings.Join(problems, "; "))
}
