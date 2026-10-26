package engine

import (
	"container/heap"
	"sort"
	"time"
)

// schedKind is what a scheduled entry is. A feature that adds a timed rule adds
// its kind here and its handler to Engine.fire: record Tick, the flush of
// feedback, the deadline of a waiting recv, the retries of the loopback.
type schedKind uint8

const (
	// schedPeriodic is a task the composition root registered to run every so
	// often: the high water mark of the clock, the renewal of the certificate,
	// the sweep of the archive, the poll of the disk.
	schedPeriodic schedKind = iota + 1
	// schedDrain is the retry of closing the records of a closed endpoint after a
	// write failed.
	schedDrain
	// schedWait is the deadline of a recv that waits.
	schedWait
	// schedOutbound is the next time to send a message again or to ask for the
	// reports of its deliveries; the id is the name of the source record.
	schedOutbound
	// schedInstanceDrain is the retry of closing the endpoints of a disabled
	// instance after a write failed; the id is the instance.
	schedInstanceDrain
	// schedFeedback is the next time the queue of feedback has a notice to send;
	// there is one entry, with no id.
	schedFeedback
	// schedFeedbackClear is the retry of taking the items a written notice covers off
	// their records after a write failed; there is one entry, with no id.
	schedFeedbackClear
	// schedTick is the next time-driven change of a record, one entry for each target
	// record and each source record; the id is "in/" and its key or "out/" and its
	// file name.
	schedTick
	// schedCreateDeadline is the end of the time a session creation has to show the
	// session ready; the id is the key of the control record.
	schedCreateDeadline
	// schedCreateRetry is the next call to the adapter of a creation that is not
	// settled; the id is the key of the control record.
	schedCreateRetry
)

// schedKey names an entry. There is at most one entry for a key, so a rule that
// moves its deadline moves its entry instead of adding one.
type schedKey struct {
	kind schedKind
	id   string
}

type schedEntry struct {
	due   time.Time
	seq   uint64
	key   schedKey
	index int
}

// scheduler is the loop's schedule: a min-heap of the entries by due time, and
// among entries due together by the order they were set, so that the sequence of
// the writes of a run does not depend on the heap and a crash point is the same
// one in two runs. It is not safe for concurrent use: the loop owns it.
//
// While held reports true, the entries of the kinds the recovery steps of the start
// own (recoveryKind) stay in the heap: next, takeDue and dueCount pass over them, so
// that the loop neither fires nor waits for them. takeDueOf is how a step takes its
// own entries, held or not.
type scheduler struct {
	h     entryHeap
	byKey map[schedKey]*schedEntry
	seq   uint64
	held  func() bool
}

// recoveryKind says whether the start's recovery steps own the entries of a kind:
// the ticks of the records, the flush of feedback and the look at a source record.
// What fell due while the node was down is done by those steps, in their order,
// and not by the schedule as soon as the loop runs.
func recoveryKind(k schedKind) bool {
	return k == schedTick || k == schedFeedback || k == schedOutbound
}

// holding says whether the entries of the recovery kinds are held now.
func (s *scheduler) holding() bool { return s.held != nil && s.held() }

// fireable says whether an entry may be given out now.
func (s *scheduler) fireable(e *schedEntry) bool { return !s.holding() || !recoveryKind(e.key.kind) }

func newScheduler() *scheduler { return &scheduler{byKey: map[schedKey]*schedEntry{}} }

// set makes the entry of the key due at due, adding it or moving it. Setting an
// entry again counts as adding it again: it goes behind the entries already due
// at the same time.
func (s *scheduler) set(key schedKey, due time.Time) {
	s.seq++
	if e, ok := s.byKey[key]; ok {
		e.due, e.seq = due, s.seq
		heap.Fix(&s.h, e.index)
		return
	}
	e := &schedEntry{due: due, seq: s.seq, key: key}
	s.byKey[key] = e
	heap.Push(&s.h, e)
}

// remove drops the entry of the key, if there is one.
func (s *scheduler) remove(key schedKey) {
	if e, ok := s.byKey[key]; ok {
		heap.Remove(&s.h, e.index)
		delete(s.byKey, key)
	}
}

// next is the earliest due time of an entry that may be given out, and whether there
// is one.
func (s *scheduler) next() (time.Time, bool) {
	if len(s.h) == 0 {
		return time.Time{}, false
	}
	if !s.holding() {
		return s.h[0].due, true
	}
	var first *schedEntry
	for _, e := range s.h {
		if s.fireable(e) && (first == nil || e.due.Before(first.due)) {
			first = e
		}
	}
	if first == nil {
		return time.Time{}, false
	}
	return first.due, true
}

// takeDue removes every entry due at now or before that may be given out and returns
// their keys in the order they are to run.
func (s *scheduler) takeDue(now time.Time) []schedKey {
	if s.holding() {
		return s.takeWhere(func(e *schedEntry) bool { return s.fireable(e) && !e.due.After(now) })
	}
	var out []schedKey
	for len(s.h) > 0 && !s.h[0].due.After(now) {
		e := heap.Pop(&s.h).(*schedEntry)
		delete(s.byKey, e.key)
		out = append(out, e.key)
	}
	return out
}

// takeDueOf is takeDue for the entries of one kind: the others stay where they are.
// It takes the entries of a held kind too: it is how the step that owns them does.
func (s *scheduler) takeDueOf(kind schedKind, now time.Time) []schedKey {
	return s.takeWhere(func(e *schedEntry) bool { return e.key.kind == kind && !e.due.After(now) })
}

// takeWhere removes the entries that match and returns their keys in the order they
// are to run.
func (s *scheduler) takeWhere(match func(*schedEntry) bool) []schedKey {
	var due []*schedEntry
	for _, e := range s.h {
		if match(e) {
			due = append(due, e)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].due.Equal(due[j].due) {
			return due[i].due.Before(due[j].due)
		}
		return due[i].seq < due[j].seq
	})
	keys := make([]schedKey, 0, len(due))
	for _, e := range due {
		heap.Remove(&s.h, e.index)
		delete(s.byKey, e.key)
		keys = append(keys, e.key)
	}
	return keys
}

// dueCount is how many entries that may be given out are due at now or before.
func (s *scheduler) dueCount(now time.Time) int {
	n := 0
	for _, e := range s.h {
		if !e.due.After(now) && s.fireable(e) {
			n++
		}
	}
	return n
}

type entryHeap []*schedEntry

func (h entryHeap) Len() int { return len(h) }
func (h entryHeap) Less(i, j int) bool {
	if !h[i].due.Equal(h[j].due) {
		return h[i].due.Before(h[j].due)
	}
	return h[i].seq < h[j].seq
}
func (h entryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *entryHeap) Push(x any) {
	e := x.(*schedEntry)
	e.index = len(*h)
	*h = append(*h, e)
}
func (h *entryHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return e
}
