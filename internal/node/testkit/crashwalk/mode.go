package crashwalk

import (
	"fmt"
	"strconv"
	"strings"
)

// Environment variables that choose how a walk runs.
const (
	EnvMode  = "SIDERA_CRASHWALK"
	EnvShard = "SIDERA_CRASHWALK_SHARD"
)

// Mode is how much of a scenario a walk covers.
type Mode int

const (
	// Smoke takes one crash point per transaction and one recovery case per
	// recovery transaction: fast enough to run before every merge.
	Smoke Mode = iota + 1
	// Full takes every crash point of the window, a kill after every step of the
	// scenario and every recovery case.
	Full
)

// ParseMode reads the value of EnvMode. Unset is Smoke: a plain test run on a
// developer's machine is fast unless it asks for more.
func ParseMode(s string) (Mode, error) {
	switch s {
	case "", "smoke":
		return Smoke, nil
	case "full":
		return Full, nil
	}
	return 0, fmt.Errorf("crashwalk: %s=%q is neither smoke nor full", EnvMode, s)
}

// Shard is one of Count equal parts of a walk, so that a walk can be spread over
// the jobs of a CI run. Index is from 1.
type Shard struct {
	Index, Count int
}

// ParseShard reads the value of EnvShard, "i/n". Unset is the whole walk.
func ParseShard(s string) (Shard, error) {
	if s == "" {
		return Shard{Index: 1, Count: 1}, nil
	}
	i, n, ok := strings.Cut(s, "/")
	if !ok {
		return Shard{}, fmt.Errorf("crashwalk: %s=%q is not i/n", EnvShard, s)
	}
	index, err1 := strconv.Atoi(i)
	count, err2 := strconv.Atoi(n)
	if err1 != nil || err2 != nil || count < 1 || index < 1 || index > count {
		return Shard{}, fmt.Errorf("crashwalk: %s=%q is not i/n with 1 <= i <= n", EnvShard, s)
	}
	return Shard{Index: index, Count: count}, nil
}

// Keep reports whether the shard runs the k-th run of the walk, counted from 0 in
// the order the walk plans them. Every run is in exactly one shard.
func (sh Shard) Keep(k int) bool {
	if sh.Count < 1 {
		return true
	}
	return k%sh.Count == sh.Index-1
}
