package fsxtest

import (
	"math/rand/v2"
	"sort"
)

// blockSize is the unit in which unsynced data reaches the disk.
const blockSize = 4096

// CrashClone returns one possible state of the disk after a power loss at this
// instant, chosen with rng; the distribution is in the package comment. Synced
// data and synced directory entries always survive. The result is what a
// restart sees: everything in it is durable, and it is independent of m.
func (m *Mem) CrashClone(rng *rand.Rand) *Mem {
	if rng == nil {
		panic("fsxtest: CrashClone needs a random source; CrashCloneWorst takes none")
	}
	return m.crash(fate{rng: rng})
}

// CrashCloneWorst returns the state of the disk in which nothing unsynced
// survived: every file is cut to its synced length and every directory is as
// its last SyncDir left it. It is deterministic and draws no random numbers.
func (m *Mem) CrashCloneWorst() *Mem { return m.crash(fate{}) }

// fate decides what happens to unsynced state. A nil rng is the worst case.
type fate struct{ rng *rand.Rand }

// keepBinding decides whether an unsynced directory entry change survives.
func (f fate) keepBinding() bool { return f.rng != nil && f.rng.IntN(2) == 0 }

// length decides how much of the unsynced tail of a file reaches the disk.
func (f fate) length(synced, current int) int {
	if f.rng == nil || synced == current {
		return synced
	}
	if f.rng.IntN(2) == 0 {
		return current
	}
	return synced + f.rng.IntN(current-synced+1)
}

type blockFate uint8

const (
	blockReal   blockFate = iota // the bytes that were written
	blockZero                    // the size grew but the data never arrived
	blockRandom                  // garbage
)

func (f fate) block() blockFate {
	switch {
	case f.rng == nil:
		return blockZero
	case f.rng.IntN(2) == 0:
		return blockReal
	case f.rng.IntN(2) == 0:
		return blockZero
	default:
		return blockRandom
	}
}

// crasher builds the post-crash tree. Walking in sorted name order makes the
// number of random draws, and so the result, a function of the seed alone.
type crasher struct {
	fate fate
	memo map[*node]*node // old node to new node: shared nodes stay shared
}

func (m *Mem) crash(f fate) *Mem {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := &crasher{fate: f, memo: map[*node]*node{}}
	return &Mem{
		root:      c.node(m.root),
		tempSeq:   m.tempSeq,
		available: m.available,
		locks:     map[string]bool{},
	}
}

func (c *crasher) node(n *node) *node {
	if done, ok := c.memo[n]; ok {
		return done
	}
	if n.dir {
		return c.dir(n)
	}
	return c.file(n)
}

func (c *crasher) file(n *node) *node {
	out := &node{mode: n.mode, wide: n.wide}
	c.memo[n] = out
	length := c.fate.length(n.synced, len(n.data))
	data := make([]byte, length)
	copy(data, n.data[:n.synced])
	for off := n.synced; off < length; {
		end := min(length, (off/blockSize+1)*blockSize)
		switch c.fate.block() {
		case blockReal:
			copy(data[off:end], n.data[off:end])
		case blockRandom:
			for i := off; i < end; i++ {
				data[i] = byte(c.fate.rng.Uint32())
			}
		}
		off = end
	}
	out.data = data
	out.synced = length
	return out
}

func (c *crasher) dir(n *node) *node {
	out := newDir()
	c.memo[n] = out

	groups := newNameGroups(n.moves)
	decided := map[string]bool{} // group representative -> the unsynced change survives
	for _, name := range unionNames(n.entries, n.syncedEntries) {
		current, synced := n.entries[name], n.syncedEntries[name]
		keep := true // identical bindings: nothing to decide
		if current != synced {
			if rep, grouped := groups.find(name); grouped {
				var ok bool
				if keep, ok = decided[rep]; !ok {
					keep = c.fate.keepBinding()
					decided[rep] = keep
				}
			} else {
				keep = c.fate.keepBinding()
			}
		}
		chosen := synced
		if keep {
			chosen = current
		}
		if chosen != nil {
			child := c.node(chosen)
			out.entries[name] = child
			out.syncedEntries[name] = child
		}
	}
	return out
}

func unionNames(a, b map[string]*node) []string {
	seen := make(map[string]bool, len(a)+len(b))
	var names []string
	for _, m := range []map[string]*node{a, b} {
		for name := range m {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// nameGroups joins the two names of every rename since the last sync, and the
// names of renames that share a name, into groups whose fate is shared: a
// single rename is atomic, so the renamed node is either moved or not.
type nameGroups map[string]string

func newNameGroups(moves []move) nameGroups {
	g := nameGroups{}
	for _, mv := range moves {
		g.union(mv.from, mv.to)
	}
	return g
}

func (g nameGroups) root(name string) string {
	for g[name] != name {
		name = g[name]
	}
	return name
}

func (g nameGroups) union(a, b string) {
	for _, n := range []string{a, b} {
		if _, ok := g[n]; !ok {
			g[n] = n
		}
	}
	ra, rb := g.root(a), g.root(b)
	if ra != rb {
		g[rb] = ra
	}
}

// find returns the group representative of name, if the name is in a group.
func (g nameGroups) find(name string) (string, bool) {
	if _, ok := g[name]; !ok {
		return "", false
	}
	return g.root(name), true
}
