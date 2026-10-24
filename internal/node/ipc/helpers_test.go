package ipc

// heldLock is an InstanceLock whose answer the test chooses.
type heldLock bool

func (h heldLock) Held() bool { return bool(h) }
