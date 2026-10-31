package adapterkit

import "errors"

// errLocked: another process holds the instance lock.
var errLocked = errors.New("adapterkit: the instance lock is held")
