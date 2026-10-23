package admin

// Level is how much a person may do on a node. Levels are ordered: each level
// includes everything the lower ones allow.
type Level int

const (
	// LevelNone grants nothing. It is the zero value and is never a valid level
	// for a list entry; it is the result when a person has no rights.
	LevelNone Level = iota
	// LevelView allows reading state, policy, endpoints, logs and diagnostics.
	LevelView
	// LevelManage allows operations that only withdraw or tighten, plus
	// day-to-day management that adds no code and lets no new node in.
	LevelManage
	// LevelFullControl additionally allows operations that let more nodes in or
	// back, run more code on the machine, or loosen internal policy. It is
	// equivalent to being the local owner for every purpose except changing the
	// authorized-persons list.
	LevelFullControl
)

// Valid reports whether l is a level that can be assigned to a list entry or
// carried by a device certificate.
func (l Level) Valid() bool { return l >= LevelView && l <= LevelFullControl }

// String returns the level's name, or "unknown" for a value outside the range.
func (l Level) String() string {
	switch l {
	case LevelNone:
		return "none"
	case LevelView:
		return "view"
	case LevelManage:
		return "manage"
	case LevelFullControl:
		return "full-control"
	default:
		return "unknown"
	}
}

// usable maps any value that is not a valid level to LevelNone, so a corrupt
// or future level read from a certificate can never grant rights.
func (l Level) usable() Level {
	if l.Valid() {
		return l
	}
	return LevelNone
}

// minLevel returns the lower of two levels after mapping invalid values to
// LevelNone.
func minLevel(a, b Level) Level {
	a, b = a.usable(), b.usable()
	if a < b {
		return a
	}
	return b
}
