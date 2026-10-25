package scenario

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/aianyai/sidera/internal/node/testkit/ledger"
)

// A Judgement is a rule about what happened that can be read from the ledger
// alone: the daemon's own state is never asked. They live here and not in the
// ledger package, which only records, and not in a fixture, which would tie them
// to one layer: both fixtures judge with the same code.
type Judgement string

// The judgements that need only entries of kinds the fakes already write. The
// rules that need a subject that is not built yet (no message lost, one
// feedback per delivery and kind, acknowledgements on behalf matching what the
// model saw, every record closed, the drain) are added with the code that makes
// that subject.
const (
	// JudgeConfirmations: an effect is on record before it is confirmed.
	JudgeConfirmations Judgement = "confirmations-precede-effects"
	// JudgeOneSessionPerAttempt: a creation attempt made at most one native
	// session.
	JudgeOneSessionPerAttempt Judgement = "one-session-per-attempt"
	// JudgePromptOncePerAttempt: a prompt attempt had its effect at most once.
	JudgePromptOncePerAttempt Judgement = "prompt-effect-once-per-attempt"
	// JudgePossiblyDuplicateMarked: a message part the model saw more than once
	// carries the possible-duplicate mark on every sighting but one.
	JudgePossiblyDuplicateMarked Judgement = "repeat-sightings-marked-possibly-duplicate"
)

type judgement struct {
	name  Judgement
	check func(*ledger.Log) []string
}

var judgements = []judgement{
	{JudgeConfirmations, func(l *ledger.Log) []string {
		if err := l.CheckConfirmations(); err != nil {
			return []string{err.Error()}
		}
		return nil
	}},
	{JudgeOneSessionPerAttempt, func(l *ledger.Log) []string {
		return overOne(l, ledger.KindRuntimeSessionCreated, func(e ledger.Entry) string { return e.Attrs.Attempt }, "attempt %s created %d native sessions")
	}},
	{JudgePromptOncePerAttempt, func(l *ledger.Log) []string {
		return overOne(l, ledger.KindRuntimePromptReceived, func(e ledger.Entry) string { return e.Attrs.Attempt }, "prompt attempt %s reached a runtime %d times")
	}},
	{JudgePossiblyDuplicateMarked, func(l *ledger.Log) []string {
		type sighting struct {
			endpoint, message string
			part              int
		}
		unmarked := map[sighting]int{}
		for _, e := range l.Kind(ledger.KindModelSaw) {
			if e.Attrs.PossiblyDuplicate != nil && *e.Attrs.PossiblyDuplicate {
				continue
			}
			unmarked[sighting{e.Attrs.Endpoint, e.Attrs.MessageID, e.Attrs.Part}]++
		}
		var out []string
		for k, n := range unmarked {
			if n > 1 {
				out = append(out, fmt.Sprintf("endpoint %s saw message %s part %d %d times without the mark", k.endpoint, k.message, k.part, n))
			}
		}
		sort.Strings(out)
		return out
	}},
}

// overOne reports every key that more than one entry of the kind has.
func overOne(l *ledger.Log, kind string, key func(ledger.Entry) string, format string) []string {
	count := map[string]int{}
	for _, e := range l.Kind(kind) {
		count[key(e)]++
	}
	var out []string
	for k, n := range count {
		if n > 1 {
			out = append(out, fmt.Sprintf(format, k, n))
		}
	}
	sort.Strings(out)
	return out
}

// Judgements lists every judgement Check knows.
func Judgements() []Judgement {
	out := make([]Judgement, len(judgements))
	for i, j := range judgements {
		out[i] = j.name
	}
	return out
}

// Invariants is what a finished run is held to. Zero holds it to every
// judgement: a script is judged by everything unless it says, with a reason, that
// a rule is one it breaks on purpose.
type Invariants struct {
	// Waive maps a judgement the script is not held to to the reason it is not.
	Waive map[Judgement]string
}

func (inv Invariants) validate() error {
	for j, reason := range inv.Waive {
		if !slices.Contains(Judgements(), j) {
			return fmt.Errorf("waiver of unknown judgement %q", j)
		}
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("waiver of judgement %q gives no reason", j)
		}
	}
	return nil
}

// Check judges the ledger and returns one error that names every judgement that
// failed, with what broke it. It is nil when every judgement that is not waived
// holds.
func Check(l *ledger.Log, inv Invariants) error {
	if err := inv.validate(); err != nil {
		return err
	}
	var problems []string
	for _, j := range judgements {
		if _, waived := inv.Waive[j.name]; waived {
			continue
		}
		for _, p := range j.check(l) {
			problems = append(problems, fmt.Sprintf("judgement %s: %s", j.name, p))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New("the ledger breaks " + strings.Join(problems, "; "))
}
