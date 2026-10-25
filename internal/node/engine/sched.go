package engine

import (
	"container/heap"
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
type scheduler struct {
	h     entryHeap
	byKey map[schedKey]*schedEntry
	seq   uint64
}

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

// next is the earliest due time, and whether there is any entry.
func (s *scheduler) next() (time.Time, bool) {
	if len(s.h) == 0 {
		return time.Time{}, false
	}
	return s.h[0].due, true
}

// takeDue removes every entry due at now or before and returns their keys in the
// order they are to run.
func (s *scheduler) takeDue(now time.Time) []schedKey {
	var out []schedKey
	for len(s.h) > 0 && !s.h[0].due.After(now) {
		e := heap.Pop(&s.h).(*schedEntry)
		delete(s.byKey, e.key)
		out = append(out, e.key)
	}
	return out
}

// dueCount is how many entries are due at now or before.
func (s *scheduler) dueCount(now time.Time) int {
	n := 0
	for _, e := range s.h {
		if !e.due.After(now) {
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
