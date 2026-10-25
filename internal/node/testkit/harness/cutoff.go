package harness

import (
	"errors"
	"sync"

	"github.com/aianyai/sidera/internal/node/fsx"
)

// errCutOff is what every call of a process answers once a crash has cut it off
// from its disk.
var errCutOff = errors.New("harness: the process was cut off from its disk by a crash")

// cutoff is the layer between a process's fault wrapper and its disk that makes a
// crash a crash: after cut, no call of the process reaches the disk, whatever
// the process is in the middle of. It is the fixture's own layer and not a rule of
// the fault wrapper: the wrapper fails a test whose rule never fires, and a
// process that was idle when it was cut has nothing to fail.
type cutoff struct {
	base fsx.FS

	// mu is held for reading by every call, from before it looks at cut until it
	// has returned, so that cut, which takes it for writing, returns only when no
	// call is half done on the disk: what the clone that follows copies is a state
	// that calls made, not one that one is making.
	mu  sync.RWMutex
	cut bool
}

var _ fsx.FS = (*cutoff)(nil)

func newCutoff(base fsx.FS) *cutoff { return &cutoff{base: base} }

// cutOff makes every later call fail and returns when the calls in flight have
// ended.
func (c *cutoff) cutOff() {
	c.mu.Lock()
	c.cut = true
	c.mu.Unlock()
}

// enter takes the read side for one call; the caller releases it with c.mu.RUnlock
// unless the process is cut off, in which case it holds nothing.
func (c *cutoff) enter() error {
	c.mu.RLock()
	if c.cut {
		c.mu.RUnlock()
		return errCutOff
	}
	return nil
}

func (c *cutoff) ReadFile(p string) ([]byte, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.mu.RUnlock()
	return c.base.ReadFile(p)
}

func (c *cutoff) ReadDir(dir string) ([]fsx.Entry, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.mu.RUnlock()
	return c.base.ReadDir(dir)
}

func (c *cutoff) Stat(p string) (fsx.Info, error) {
	if err := c.enter(); err != nil {
		return fsx.Info{}, err
	}
	defer c.mu.RUnlock()
	return c.base.Stat(p)
}

func (c *cutoff) Available(dir string) (uint64, error) {
	if err := c.enter(); err != nil {
		return 0, err
	}
	defer c.mu.RUnlock()
	return c.base.Available(dir)
}

func (c *cutoff) Lock(p string) (func() error, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.mu.RUnlock()
	return c.base.Lock(p)
}

func (c *cutoff) CreateTemp(tag fsx.Tag, dir string, mode fsx.Mode) (fsx.File, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.mu.RUnlock()
	f, err := c.base.CreateTemp(tag, dir, mode)
	if err != nil {
		return nil, err
	}
	return &cutFile{File: f, c: c}, nil
}

func (c *cutoff) OpenAppend(tag fsx.Tag, p string) (fsx.File, error) {
	if err := c.enter(); err != nil {
		return nil, err
	}
	defer c.mu.RUnlock()
	f, err := c.base.OpenAppend(tag, p)
	if err != nil {
		return nil, err
	}
	return &cutFile{File: f, c: c}, nil
}

func (c *cutoff) Rename(tag fsx.Tag, from, to string) error {
	if err := c.enter(); err != nil {
		return err
	}
	defer c.mu.RUnlock()
	return c.base.Rename(tag, from, to)
}

func (c *cutoff) SyncDir(tag fsx.Tag, dir string) error {
	if err := c.enter(); err != nil {
		return err
	}
	defer c.mu.RUnlock()
	return c.base.SyncDir(tag, dir)
}

func (c *cutoff) Remove(tag fsx.Tag, p string) error {
	if err := c.enter(); err != nil {
		return err
	}
	defer c.mu.RUnlock()
	return c.base.Remove(tag, p)
}

func (c *cutoff) RemoveAll(tag fsx.Tag, dir string) error {
	if err := c.enter(); err != nil {
		return err
	}
	defer c.mu.RUnlock()
	return c.base.RemoveAll(tag, dir)
}

func (c *cutoff) Mkdir(tag fsx.Tag, dir string) error {
	if err := c.enter(); err != nil {
		return err
	}
	defer c.mu.RUnlock()
	return c.base.Mkdir(tag, dir)
}

func (c *cutoff) RenameDir(tag fsx.Tag, from, to string) error {
	if err := c.enter(); err != nil {
		return err
	}
	defer c.mu.RUnlock()
	return c.base.RenameDir(tag, from, to)
}

// cutFile is a file whose writes and syncs are cut off with its process. Close is
// not a step and is passed on: a process that cannot write can still let go.
type cutFile struct {
	fsx.File
	c *cutoff
}

func (f *cutFile) Write(p []byte) (int, error) {
	if err := f.c.enter(); err != nil {
		return 0, err
	}
	defer f.c.mu.RUnlock()
	return f.File.Write(p)
}

func (f *cutFile) Sync() error {
	if err := f.c.enter(); err != nil {
		return err
	}
	defer f.c.mu.RUnlock()
	return f.File.Sync()
}
