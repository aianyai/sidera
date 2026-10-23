package record

import (
	"fmt"

	"github.com/aianyai/sidera/internal/admin"
)

// Wire values of the administration levels. They are an independent table, not
// the in-memory values of admin.Level, so that reordering or extending the Go
// constants never changes what a signed certificate means.
const (
	wireLevelView        = 1
	wireLevelManage      = 2
	wireLevelFullControl = 3
)

// LevelToWire returns the wire value of l. LevelNone and unknown values are
// refused: a certificate states its cap explicitly, with 3 for "no cap".
func LevelToWire(l admin.Level) (uint64, error) {
	switch l {
	case admin.LevelView:
		return wireLevelView, nil
	case admin.LevelManage:
		return wireLevelManage, nil
	case admin.LevelFullControl:
		return wireLevelFullControl, nil
	}
	return 0, fmt.Errorf("%w: %v", ErrLevel, l)
}

// LevelFromWire returns the level for a wire value: 1 view, 2 manage, 3 full
// control. Zero and every other value are refused.
func LevelFromWire(v uint64) (admin.Level, error) {
	switch v {
	case wireLevelView:
		return admin.LevelView, nil
	case wireLevelManage:
		return admin.LevelManage, nil
	case wireLevelFullControl:
		return admin.LevelFullControl, nil
	}
	return admin.LevelNone, fmt.Errorf("%w: wire value %d", ErrLevel, v)
}
