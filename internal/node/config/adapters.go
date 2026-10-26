package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/txn"
	"github.com/aianyai/sidera/sdk/go/adapterproto"
	"github.com/aianyai/sidera/sdk/go/manifest"
)

// StageSuffixBytes is how much randomness StageAdapter reads from its reader to
// name a staging directory. A caller that may not let the staging read its own
// source of randomness (the engine draws only in its loop, and staging runs
// outside it) draws this many bytes first and hands them over.
const StageSuffixBytes = 8

// reservedName reports whether a name inside a package would be taken for the
// debris of an interrupted operation: the cleanup at start removes the
// temporary and trash names anywhere, and the repair of the adapter directory
// removes the staging names, so a package that held one would lose it silently.
func reservedName(name string) bool {
	return strings.HasPrefix(name, fsx.TempPrefix) || strings.HasPrefix(name, fsx.TrashPrefix) || strings.HasPrefix(name, stagePrefix)
}

// SourceError is a package that cannot be read: Path is the directory or file
// inside the package whose reading failed, relative to the package, "." for the
// package itself. The package is not what it should be, and the owner who named
// it can fix it; that is no failure of this node's own storage.
type SourceError struct {
	Path string
	Err  error
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("config: reading %q of the package: %v", e.Path, e.Err)
}

func (e *SourceError) Unwrap() error { return e.Err }

func readAdapters(r fsx.Reader, st *state) error {
	entries, err := listDir(r, AdaptersDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		id := e.Name
		if !e.IsDir || adapterproto.ValidateAdapterID(id) != nil {
			return &FileError{Path: AdapterDir(id), Kind: KindDir, Reason: FileMisnamed,
				Err: errors.New("an entry here is the directory of an adapter, named by its id")}
		}
		data, err := r.ReadFile(AdapterCurrentPath(id))
		if missing(err) {
			// An installation that did not finish. It is not installed, and the
			// repair removes it; it is never a reason to refuse to start.
			continue
		}
		if err != nil {
			return &FileError{Path: AdapterCurrentPath(id), Kind: KindAdapterCurrent, Reason: FileUnreadable, Err: err}
		}
		version, err := DecodeAdapterCurrent(id, data)
		if err != nil {
			return err
		}
		mpath := AdapterManifestPath(id, version)
		mdata, err := readFile(r, mpath, KindAdapterManifest)
		if err != nil {
			return err
		}
		m, err := manifest.Parse(mdata)
		if err != nil {
			return &FileError{Path: mpath, Kind: KindAdapterManifest, Reason: FileInvalid, Err: err}
		}
		if m.ID != id || m.Version != version {
			return &FileError{Path: mpath, Kind: KindAdapterManifest, Reason: FileMisnamed,
				Err: fmt.Errorf("the manifest is of %s %s, the directory is %s %s", m.ID, m.Version, id, version)}
		}
		st.adapters[id] = Adapter{ID: id, Version: version, Manifest: m}
	}
	return nil
}

// AdapterGCWork lists what the repair of the adapter directory removes: an
// adapter directory without a current pointer, and in an installed adapter a
// staging directory or a version directory the pointer does not name. It only
// reads, and it is the one definition of that garbage: the repair removes
// exactly this list, and a test evaluates it on a copy of the disk, where a run
// without a crash must leave it empty at every quiet point. The order is by
// adapter id and, within an adapter, by name.
func AdapterGCWork(r fsx.Reader) ([]GCItem, error) {
	entries, err := listDir(r, AdaptersDir)
	if err != nil {
		return nil, err
	}
	var work []GCItem
	for _, e := range entries {
		id := e.Name
		if !e.IsDir || adapterproto.ValidateAdapterID(id) != nil {
			continue
		}
		data, err := r.ReadFile(AdapterCurrentPath(id))
		if missing(err) {
			work = append(work, GCItem{Path: AdapterDir(id), Reason: GCNotInstalled})
			continue
		}
		if err != nil {
			return nil, &FileError{Path: AdapterCurrentPath(id), Kind: KindAdapterCurrent, Reason: FileUnreadable, Err: err}
		}
		version, err := DecodeAdapterCurrent(id, data)
		if err != nil {
			return nil, err
		}
		versions, err := r.ReadDir(AdapterVersionsDir(id))
		if err != nil {
			if missing(err) {
				continue
			}
			return nil, &FileError{Path: AdapterVersionsDir(id), Kind: KindDir, Reason: FileUnreadable, Err: err}
		}
		for _, v := range versions {
			switch {
			case !v.IsDir || fsx.IsLeftover(v.Name):
			case strings.HasPrefix(v.Name, stagePrefix):
				work = append(work, GCItem{Path: path.Join(AdapterVersionsDir(id), v.Name), Reason: GCStaging})
			case v.Name != version:
				work = append(work, GCItem{Path: path.Join(AdapterVersionsDir(id), v.Name), Reason: GCSuperseded})
			}
		}
	}
	return work, nil
}

// repairAdapters removes the garbage AdapterGCWork lists, in one Recovery
// transaction. With nothing to remove it does nothing and reaches no point, so
// a start on a clean disk is not a recovery.
func (c *Config) repairAdapters() error {
	work, err := AdapterGCWork(c.w.Reader())
	if err != nil || len(work) == 0 {
		return err
	}
	tx := c.w.Begin(adapterGC)
	defer tx.Close()
	for _, item := range work {
		if err := tx.Discard(RoleDir, item.Path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// pkg is a package directory read through a Reader rooted at it.
type pkg struct {
	manifest manifest.Manifest
	dirs     []string // relative paths, each after its parent
	files    []string // relative paths
}

func readPackage(src fsx.Reader) (pkg, error) {
	var p pkg
	data, err := src.ReadFile(manifest.FileName)
	if err != nil {
		return p, &ChangeError{Reason: ChangeInvalid, Field: manifest.FileName, Err: fmt.Errorf("the package has no readable manifest: %w", err)}
	}
	if p.manifest, err = manifest.Parse(data); err != nil {
		return p, &ChangeError{Reason: ChangeInvalid, Field: manifest.FileName, Err: err}
	}
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := src.ReadDir(dir)
		if err != nil {
			return &SourceError{Path: dir, Err: err}
		}
		for _, e := range entries {
			rel := path.Join(dir, e.Name)
			if dir == "." {
				rel = e.Name
			}
			if reservedName(e.Name) {
				return &ChangeError{Reason: ChangeInvalid, Field: rel, Err: fmt.Errorf("a name that begins with %q, %q or %q would be taken for debris and removed",
					fsx.TempPrefix, fsx.TrashPrefix, stagePrefix)}
			}
			if e.IsDir {
				p.dirs = append(p.dirs, rel)
				if err := walk(rel); err != nil {
					return err
				}
				continue
			}
			p.files = append(p.files, rel)
		}
		return nil
	}
	if err := walk("."); err != nil {
		return p, err
	}
	if !slices.Contains(p.files, path.Clean(p.manifest.Entry.Program)) {
		return p, &ChangeError{Reason: ChangeInvalid, Field: "entry.program", Err: fmt.Errorf("the package holds no file %q", p.manifest.Entry.Program)}
	}
	return p, nil
}

// StageAdapter copies the package that src is rooted at into a staging
// directory next to the versions of its adapter, and parses the manifest of the
// copy. Nothing is installed: everything it writes is preparation that the
// repair of the adapter directory removes after a crash, and that Unstage
// removes when the installation is refused. The copy is what is checked, not
// the source, so the package cannot change between the check and the install.
//
// The package is read before anything is written, so a package that is refused
// for what is in it leaves nothing. rnd names the staging directory. The
// platform the manifest lists is the caller's to check on Staged.Manifest.
func (c *Config) StageAdapter(src fsx.Reader, rnd io.Reader) (Staged, error) {
	p, err := readPackage(src)
	if err != nil {
		return Staged{}, err
	}
	var suffix [StageSuffixBytes]byte
	if _, err := io.ReadFull(rnd, suffix[:]); err != nil {
		return Staged{}, fmt.Errorf("config: naming the staging directory: %w", err)
	}
	id, version := p.manifest.ID, p.manifest.Version
	staged := Staged{ID: id, Version: version, Dir: AdapterStageDir(id, hex.EncodeToString(suffix[:]))}

	fail := func(err error) (Staged, error) {
		if !txn.IsFatal(err) {
			// Best effort: whatever this leaves, the next start removes.
			_ = c.unstage(staged)
		}
		return Staged{}, err
	}
	if err := c.copyPackage(src, p, staged); err != nil {
		return fail(err)
	}
	data, err := c.w.Reader().ReadFile(path.Join(staged.Dir, manifest.FileName))
	if err != nil {
		return fail(fmt.Errorf("config: reading the staged manifest: %w", err))
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return fail(&ChangeError{Reason: ChangeInvalid, Field: manifest.FileName, Err: err})
	}
	if m.ID != id || m.Version != version {
		return fail(&ChangeError{Reason: ChangeInvalid, Field: manifest.FileName,
			Err: fmt.Errorf("the manifest was %s %s when the package was read and %s %s in the copy: the package changed while it was copied", id, version, m.ID, m.Version)})
	}
	staged.Manifest = m
	return staged, nil
}

func (c *Config) copyPackage(src fsx.Reader, p pkg, staged Staged) error {
	r := c.w.Reader()
	tx := c.w.Begin(adapterStage)
	defer tx.Close()
	for _, d := range []string{AdapterDir(staged.ID), AdapterVersionsDir(staged.ID)} {
		there, err := fsx.Exists(r, d)
		if err != nil {
			return err
		}
		if !there {
			if err := tx.MakeDir(roleParent, d); err != nil {
				return err
			}
		}
	}
	if err := tx.MakeDir(roleStage, staged.Dir); err != nil {
		return err
	}
	for _, d := range p.dirs {
		if err := tx.MakeDir(roleSubdir, path.Join(staged.Dir, d)); err != nil {
			return err
		}
	}
	program := path.Clean(p.manifest.Entry.Program)
	for _, f := range p.files {
		data, err := src.ReadFile(f)
		if err != nil {
			return &SourceError{Path: f, Err: err}
		}
		mode := fsx.ModePrivate
		if f == program {
			mode = fsx.ModePrivateExec
		}
		if err := tx.Replace(roleFile, path.Join(staged.Dir, f), data, mode); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Unstage drops what an installation that did not complete left: the staging
// directory; a version directory that was renamed in and did not become
// current; and, when the adapter was never installed, its whole directory. It
// never touches the version the current pointer names. What is not there is not
// an error, and with nothing to drop it writes nothing.
func (c *Config) Unstage(s Staged) error {
	return c.unstage(s)
}

func (c *Config) unstage(s Staged) error {
	r := c.w.Reader()
	current, hasCurrent := "", false
	switch data, err := r.ReadFile(AdapterCurrentPath(s.ID)); {
	case err == nil:
		v, derr := DecodeAdapterCurrent(s.ID, data)
		if derr != nil {
			return derr
		}
		current, hasCurrent = v, true
	case !missing(err):
		return &FileError{Path: AdapterCurrentPath(s.ID), Kind: KindAdapterCurrent, Reason: FileUnreadable, Err: err}
	}
	versionDir := AdapterVersionDir(s.ID, s.Version)
	dropStage, err := fsx.Exists(r, s.Dir)
	if err != nil {
		return err
	}
	dropVersion, dropParent := false, false
	if hasCurrent && current != s.Version {
		if dropVersion, err = fsx.Exists(r, versionDir); err != nil {
			return err
		}
	}
	if !hasCurrent {
		if dropParent, err = fsx.Exists(r, AdapterDir(s.ID)); err != nil {
			return err
		}
	}
	if !dropStage && !dropVersion && !dropParent {
		return nil
	}
	tx := c.w.Begin(adapterUnstage)
	defer tx.Close()
	if dropStage {
		if err := tx.Discard(roleStage, s.Dir); err != nil {
			return err
		}
	}
	if dropVersion {
		if err := tx.Discard(RoleVersion, versionDir); err != nil {
			return err
		}
	}
	if dropParent {
		if err := tx.Discard(roleParent, AdapterDir(s.ID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Install is the plan of the writes that make a staged package the current
// version of its adapter. The composing package runs them in its own
// transaction, in this order, with its own write between Point and DiscardOld:
// DiscardStale, Promote, Point, DiscardOld. The commit point is the replacement
// of the current pointer. PlanInstall, before the transaction begins, does every
// check and encoding the writes need, so none of them can fail for a reason of
// its own.
type Install struct {
	// AlreadyCurrent is true when the staged version is the one that is current:
	// there is nothing to write, and the installation is already done.
	AlreadyCurrent bool

	c        *Config
	staged   Staged
	stale    bool   // a directory of this version exists and is not current
	previous string // the version that is current now, "" for a first installation
	current  []byte // the pointer file of the staged version, encoded by PlanInstall
}

var errAlreadyCurrent = errors.New("config: the staged version is already current")

// StillEnding says whether a process of the instance is still being ended. Config
// does not run adapters, so the caller that does tells it.
type StillEnding func(instance string) bool

// PlanInstall plans the installation of a staged package. An adapter that has
// an enabled instance is not upgraded: the instance runs the version that is
// current, so the change is refused with ChangeAdapterInUse until the owner has
// disabled it. Neither is one whose disabled instance still has a process ending
// (ending says so; nil says none does): the process may hold the files of the
// version that the installation discards, which a running executable does not
// allow on every platform. A first installation has no such instance to disturb.
func (c *Config) PlanInstall(s Staged, ending StillEnding) (Install, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	st := c.read()
	plan := Install{c: c, staged: s}
	if cur, ok := st.adapters[s.ID]; ok {
		if cur.Version == s.Version {
			plan.AlreadyCurrent = true
			return plan, nil
		}
		for _, name := range sortedKeys(st.instances.Items) {
			in := st.instances.Items[name]
			switch {
			case in.Adapter != s.ID:
			case in.Enabled:
				return Install{}, &ChangeError{Reason: ChangeAdapterInUse, Field: "instances." + name,
					Err: fmt.Errorf("the instance %q is enabled and runs version %s", name, cur.Version)}
			case ending != nil && ending(name):
				return Install{}, &ChangeError{Reason: ChangeAdapterInUse, Field: "instances." + name,
					Err: fmt.Errorf("the process of the disabled instance %q, which runs version %s, is still ending", name, cur.Version)}
			}
		}
		plan.previous = cur.Version
	}
	stale, err := fsx.Exists(c.w.Reader(), AdapterVersionDir(s.ID, s.Version))
	if err != nil {
		return Install{}, err
	}
	plan.stale = stale
	if plan.current, err = EncodeAdapterCurrent(s.Version); err != nil {
		return Install{}, err
	}
	return plan, nil
}

// DiscardStale discards a leftover directory of the version, under RoleStale:
// the staging directory is renamed to that name next, and the rename needs the
// name to be free. It writes nothing when there is none.
func (i Install) DiscardStale(tx *txn.Tx) error {
	if i.AlreadyCurrent {
		return errAlreadyCurrent
	}
	if !i.stale {
		return nil
	}
	return tx.Discard(RoleStale, AdapterVersionDir(i.staged.ID, i.staged.Version))
}

// Promote renames the staging directory to the version, under RoleVersion.
func (i Install) Promote(tx *txn.Tx) error {
	if i.AlreadyCurrent {
		return errAlreadyCurrent
	}
	return tx.RenameDir(RoleVersion, i.staged.Dir, AdapterVersionDir(i.staged.ID, i.staged.Version))
}

// Point replaces the current pointer, under RoleCurrent. This is the commit
// point of the installation. The returned function makes the configuration in
// memory take the version over; it is called after the transaction committed.
func (i Install) Point(tx *txn.Tx) (AfterCommit, error) {
	if i.AlreadyCurrent {
		return nil, errAlreadyCurrent
	}
	if err := tx.Replace(RoleCurrent, AdapterCurrentPath(i.staged.ID), i.current, fsx.ModePrivate); err != nil {
		return nil, err
	}
	adapter := Adapter{ID: i.staged.ID, Version: i.staged.Version, Manifest: cloneManifest(i.staged.Manifest)}
	return func() {
		i.c.mu.Lock()
		defer i.c.mu.Unlock()
		adapters := make(map[string]Adapter, len(i.c.st.adapters)+1)
		for k, v := range i.c.st.adapters {
			adapters[k] = v
		}
		adapters[adapter.ID] = adapter
		i.c.st.adapters = adapters
	}, nil
}

// DiscardOld discards the previous version, under RoleOld. It writes nothing for
// a first installation. No process uses it: PlanInstall refused to replace a
// version an enabled instance runs.
func (i Install) DiscardOld(tx *txn.Tx) error {
	if i.AlreadyCurrent {
		return errAlreadyCurrent
	}
	if i.previous == "" {
		return nil
	}
	return tx.Discard(RoleOld, AdapterVersionDir(i.staged.ID, i.previous))
}
