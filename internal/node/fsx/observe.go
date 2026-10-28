package fsx

// Observation binds a point to the actual operands of that arrival. Metadata is
// not part of the point's identity or its counter key.
type Observation struct {
	Point Point // identity and ordinal of this arrival
	// Path is the primitive's path, or the target of a rename. CreateTemp reports
	// its parent directory: the temporary file has not been named yet. File Write
	// and Sync retain the File.Name returned when the handle was opened.
	Path string
	// From is the source of Rename or RenameDir, and empty for other steps.
	From string
}

// observed numbers every write-type step and tells a hook before it runs.
type observed struct {
	base FS
	c    *Counter
	hook func(Observation) error
}

// Observe wraps fs so that every write-type step, including the Write and Sync
// of files opened through it, is numbered with c and passed to hook before it
// runs, with its actual path operands in the same Observation. A nil hook only
// counts. A non-nil error from hook skips the step and is returned. Reads and
// Lock are not steps. Steps tagged with a reserved transaction (TxLog,
// TxCleanup) are neither numbered nor passed to the hook.
//
// The returned errors of observed steps carry their point (see PointOf). A
// retry inside the platform layer below is not a new step: one logical step
// reaches the hook once.
func Observe(fs FS, c *Counter, hook func(Observation) error) FS {
	return &observed{base: fs, c: c, hook: hook}
}

// before numbers the step and runs the hook. It returns the point, whether the
// step is observed at all, and the hook's error.
func (o *observed) before(tag Tag, step Step, path, from string) (Point, bool, error) {
	if tag.Reserved() {
		return Point{}, false, nil
	}
	p := o.c.Next(Key{Tag: tag, Step: step})
	if o.hook == nil {
		return p, true, nil
	}
	return p, true, o.hook(Observation{Point: p, Path: path, From: from})
}

// stamp attaches the point to an error of an observed step.
func stamp(p Point, observed bool, err error) error {
	if err == nil || !observed {
		return err
	}
	return &stepError{point: p, err: err}
}

func (o *observed) ReadFile(p string) ([]byte, error) { return o.base.ReadFile(p) }
func (o *observed) ReadFileLimit(p string, maxBytes int64) ([]byte, error) {
	return o.base.ReadFileLimit(p, maxBytes)
}
func (o *observed) ReadDir(dir string) ([]Entry, error)  { return o.base.ReadDir(dir) }
func (o *observed) Stat(p string) (Info, error)          { return o.base.Stat(p) }
func (o *observed) Available(dir string) (uint64, error) { return o.base.Available(dir) }

func (o *observed) Lock(p string) (func() error, error) { return o.base.Lock(p) }

func (o *observed) CreateTemp(tag Tag, dir string, mode Mode) (File, error) {
	pt, ok, err := o.before(tag, StepCreate, dir, "")
	if err != nil {
		return nil, stamp(pt, ok, err)
	}
	f, err := o.base.CreateTemp(tag, dir, mode)
	if err != nil {
		return nil, stamp(pt, ok, err)
	}
	return &observedFile{File: f, o: o, tag: tag, name: f.Name()}, nil
}

func (o *observed) OpenAppend(tag Tag, p string) (File, error) {
	pt, ok, err := o.before(tag, StepOpen, p, "")
	if err != nil {
		return nil, stamp(pt, ok, err)
	}
	f, err := o.base.OpenAppend(tag, p)
	if err != nil {
		return nil, stamp(pt, ok, err)
	}
	return &observedFile{File: f, o: o, tag: tag, name: f.Name()}, nil
}

// run is the shape of every write-type FS method without a result.
func (o *observed) run(tag Tag, step Step, path, from string, do func() error) error {
	pt, ok, err := o.before(tag, step, path, from)
	if err != nil {
		return stamp(pt, ok, err)
	}
	return stamp(pt, ok, do())
}

func (o *observed) Rename(tag Tag, from, to string) error {
	return o.run(tag, StepRename, to, from, func() error { return o.base.Rename(tag, from, to) })
}

func (o *observed) SyncDir(tag Tag, dir string) error {
	return o.run(tag, StepSyncDir, dir, "", func() error { return o.base.SyncDir(tag, dir) })
}

func (o *observed) Remove(tag Tag, p string) error {
	return o.run(tag, StepRemove, p, "", func() error { return o.base.Remove(tag, p) })
}

func (o *observed) RemoveAll(tag Tag, dir string) error {
	return o.run(tag, StepRemoveAll, dir, "", func() error { return o.base.RemoveAll(tag, dir) })
}

func (o *observed) Mkdir(tag Tag, dir string) error {
	return o.run(tag, StepMkdir, dir, "", func() error { return o.base.Mkdir(tag, dir) })
}

func (o *observed) RenameDir(tag Tag, from, to string) error {
	return o.run(tag, StepRenameDir, to, from, func() error { return o.base.RenameDir(tag, from, to) })
}

// observedFile attributes Write and Sync to the tag the file was opened with.
type observedFile struct {
	File
	o    *observed
	tag  Tag
	name string // retained when opened, even if the file is renamed
}

func (f *observedFile) Write(p []byte) (int, error) {
	pt, ok, err := f.o.before(f.tag, StepWrite, f.name, "")
	if err != nil {
		return 0, stamp(pt, ok, err)
	}
	n, err := f.File.Write(p)
	return n, stamp(pt, ok, err)
}

func (f *observedFile) Sync() error {
	return f.o.run(f.tag, StepSync, f.name, "", f.File.Sync)
}
