package fsx

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Step is one crash-relevant action. Each write-type FS call is exactly one
// step; StepCommit is emitted by the transaction layer, never by an FS.
type Step uint8

const (
	StepCreate    Step = iota + 1 // create an exclusive temporary file
	StepOpen                      // open a file for appending, creating it if absent
	StepWrite                     // write to an open file
	StepSync                      // fsync an open file
	StepRename                    // rename a file within its directory, replacing the target
	StepSyncDir                   // fsync a directory
	StepRemove                    // unlink a file
	StepRemoveAll                 // remove a directory tree
	StepMkdir                     // create one directory
	StepRenameDir                 // rename a directory within its parent; the target must not exist
	StepCommit                    // the transaction's last step completed; nothing has been acknowledged yet
)

var stepNames = [...]string{
	StepCreate:    "create",
	StepOpen:      "open",
	StepWrite:     "write",
	StepSync:      "sync",
	StepRename:    "rename",
	StepSyncDir:   "sync-dir",
	StepRemove:    "remove",
	StepRemoveAll: "remove-all",
	StepMkdir:     "mkdir",
	StepRenameDir: "rename-dir",
	StepCommit:    "commit",
}

// String returns the step's name in the crash point vocabulary.
func (s Step) String() string {
	if s >= StepCreate && s <= StepCommit {
		return stepNames[s]
	}
	return "step(" + strconv.Itoa(int(s)) + ")"
}

// ParseStep is the inverse of Step.String for the fixed vocabulary.
func ParseStep(name string) (Step, error) {
	for s := StepCreate; s <= StepCommit; s++ {
		if stepNames[s] == name {
			return s, nil
		}
	}
	return 0, fmt.Errorf("fsx: unknown step %q", name)
}

// Key identifies a crash point without its ordinal; Point adds the ordinal.
type Key struct {
	Tag  Tag
	Step Step
}

// Point is a crash point: a key and the ordinal of this arrival.
type Point struct {
	Key
	Hit int // 1-based: the n-th time this key was reached by this counter
}

// Validate reports whether the key can be written and parsed back: both names
// are valid, the step is in the vocabulary, and the role is empty exactly for
// StepCommit.
func (k Key) Validate() error {
	if k.Step < StepCreate || k.Step > StepCommit {
		return fmt.Errorf("fsx: unknown step %d", k.Step)
	}
	if !ValidName(k.Tag.Tx) {
		return fmt.Errorf("fsx: invalid transaction name %q", k.Tag.Tx)
	}
	if k.Step == StepCommit {
		if k.Tag.Role != "" {
			return fmt.Errorf("fsx: a commit point has no role, got %q", k.Tag.Role)
		}
		return nil
	}
	if !ValidName(k.Tag.Role) {
		return fmt.Errorf("fsx: invalid role name %q", k.Tag.Role)
	}
	return nil
}

// Validate is Key.Validate plus the requirement that the ordinal is positive.
func (p Point) Validate() error {
	if err := p.Key.Validate(); err != nil {
		return err
	}
	if p.Hit < 1 {
		return fmt.Errorf("fsx: ordinal %d is not positive", p.Hit)
	}
	return nil
}

// String formats the key as "<tx>/<role>.<step>", or "<tx>/commit".
func (k Key) String() string {
	if k.Step == StepCommit {
		return k.Tag.Tx + "/" + k.Step.String()
	}
	return k.Tag.Tx + "/" + k.Tag.Role + "." + k.Step.String()
}

// String formats the point as its key followed by "#<ordinal>".
func (p Point) String() string { return p.Key.String() + "#" + strconv.Itoa(p.Hit) }

// ParseKey is the inverse of Key.String. It accepts only canonical text.
func ParseKey(s string) (Key, error) {
	tx, rest, ok := strings.Cut(s, "/")
	if !ok {
		return Key{}, fmt.Errorf("fsx: crash point key %q has no '/'", s)
	}
	var k Key
	k.Tag.Tx = tx
	if rest == StepCommit.String() {
		k.Step = StepCommit
	} else {
		role, step, ok := strings.Cut(rest, ".")
		if !ok {
			return Key{}, fmt.Errorf("fsx: crash point key %q has no '.' before the step", s)
		}
		st, err := ParseStep(step)
		if err != nil {
			return Key{}, err
		}
		k.Tag.Role, k.Step = role, st
	}
	if err := k.Validate(); err != nil {
		return Key{}, err
	}
	return k, nil
}

// ParsePoint is the inverse of Point.String. It accepts only canonical text:
// an ordinal has no sign and no leading zero.
func ParsePoint(s string) (Point, error) {
	key, ord, ok := strings.Cut(s, "#")
	if !ok {
		return Point{}, fmt.Errorf("fsx: crash point %q has no '#' ordinal", s)
	}
	k, err := ParseKey(key)
	if err != nil {
		return Point{}, err
	}
	if ord == "" || ord[0] == '0' || strings.Trim(ord, "0123456789") != "" {
		return Point{}, fmt.Errorf("fsx: crash point %q has a malformed ordinal", s)
	}
	hit, err := strconv.Atoi(ord)
	if err != nil {
		return Point{}, fmt.Errorf("fsx: crash point %q has an ordinal out of range", s)
	}
	return Point{Key: k, Hit: hit}, nil
}

// Counter assigns ordinals to keys. It is safe for concurrent use.
type Counter struct {
	mu   sync.Mutex
	hits map[Key]int
}

// Next returns the point for the next arrival at k, numbered from 1.
func (c *Counter) Next(k Key) Point {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hits == nil {
		c.hits = make(map[Key]int)
	}
	c.hits[k]++
	return Point{Key: k, Hit: c.hits[k]}
}
