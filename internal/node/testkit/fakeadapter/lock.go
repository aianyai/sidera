package fakeadapter

import "errors"

// errLocked: another process holds the instance lock.
var errLocked = errors.New("fakeadapter: the instance lock is held")
