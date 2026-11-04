package config

import (
	"bytes"
	"crypto/sha256"
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

// MaxPackageBytes is the most a package may hold in all, the sizes of its files
// added up. The whole package is read through a file system the owner points at,
// one file at a time into memory, so a package without a limit could take the node
// down by being large; no adapter is anywhere near it, so the limit is a refusal of
// a mistake (the wrong directory) and never of a package.
const MaxPackageBytes = 256 << 20

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

// readAdapters loads the installed adapters. An entry of adapters/ that is no
// adapter directory refuses startup, as a stray entry anywhere in config/ does. A
// package that cannot be loaded does not: its adapter is kept with the Fault that
// says why, because one bad package must not stop the node and the adapters beside
// it, and installing the package again repairs it.
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
		a, installed := readAdapter(r, id)
		if installed {
			st.adapters[id] = a
		}
	}
	return nil
}

// asFileError returns the *FileError that readFile and DecodeAdapterCurrent make of
// every failure.
func asFileError(err error) *FileError {
	var fe *FileError
	errors.As(err, &fe)
	return fe
}

// readAdapter loads one adapter. It says false for a directory with no current
// pointer, an installation that did not finish: that is not installed, and the
// repair removes it.
func readAdapter(r fsx.Reader, id string) (Adapter, bool) {
	data, err := r.ReadFile(AdapterCurrentPath(id))
	if missing(err) {
		return Adapter{}, false
	}
	if err != nil {
		return Adapter{ID: id, Fault: &FileError{Path: AdapterCurrentPath(id), Kind: KindAdapterCurrent, Reason: FileUnreadable, Err: err}}, true
	}
	cur, err := DecodeAdapterCurrent(id, data)
	if err != nil {
		return Adapter{ID: id, Fault: asFileError(err)}, true
	}
	a := Adapter{ID: id, Version: cur.Version, Digest: cur.Digest, Revision: cur.Revision}
	mpath := path.Join(AdapterRevisionDir(id, cur.Version, cur.Revision), manifest.FileName)
	mdata, err := readFile(r, mpath, KindAdapterManifest)
	if err != nil {
		a.Fault = asFileError(err)
		return a, true
	}
	m, err := manifest.Parse(mdata)
	if err != nil {
		a.Fault = &FileError{Path: mpath, Kind: KindAdapterManifest, Reason: FileInvalid, Err: err}
		return a, true
	}
	if m.ID != id || m.Version != cur.Version {
		a.Fault = &FileError{Path: mpath, Kind: KindAdapterManifest, Reason: FileMisnamed,
			Err: fmt.Errorf("the manifest is of %s %s, the directory is %s %s", m.ID, m.Version, id, cur.Version)}
		return a, true
	}
	a.Manifest = m
	return a, true
}

// AdapterGCWork lists what the repair of the adapter directory removes: an
// adapter directory without a current pointer, and in an installed adapter a
// staging directory or a version directory the pointer does not name. It only
// reads, and it is the one definition of that garbage: the repair removes
// exactly this list (an adapter whose pointer cannot be read is left alone, for
// what to keep is not known), and a test evaluates it on a copy of the disk, where a run
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
			continue // which version is kept is not known: the adapter is faulted, and a new installation repairs it
		}
		cur, err := DecodeAdapterCurrent(id, data)
		if err != nil {
			continue
		}
		keep := path.Base(AdapterRevisionDir(id, cur.Version, cur.Revision))
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
			case v.Name != keep:
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
	// raw is the manifest as it was read, to compare with the copy's.
	raw   []byte
	dirs  []string // relative paths, each after its parent
	files []string // relative paths
}

// errPackageTooLarge is the refusal of a package that holds more than
// MaxPackageBytes; file is the one that went over.
func errPackageTooLarge(file string) error {
	return &ChangeError{Reason: ChangeInvalid, Field: file, Err: fmt.Errorf("the package holds more than %d bytes (%d MiB) in all", int64(MaxPackageBytes), MaxPackageBytes>>20)}
}

func readPackage(src fsx.Reader) (pkg, error) {
	var p pkg
	data, err := src.ReadFileLimit(manifest.FileName, MaxPackageBytes)
	if errors.Is(err, fsx.ErrTooLarge) {
		return p, errPackageTooLarge(manifest.FileName)
	}
	if err != nil {
		return p, &ChangeError{Reason: ChangeInvalid, Field: manifest.FileName, Err: fmt.Errorf("the package has no readable manifest: %w", err)}
	}
	if p.manifest, err = manifest.Parse(data); err != nil {
		return p, &ChangeError{Reason: ChangeInvalid, Field: manifest.FileName, Err: err}
	}
	p.raw = data
	var total int64
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
			// Stat describes a link itself, whose length says nothing about the
			// target's bytes. Those are counted by the bounded read of the copy.
			if !e.IsSymlink {
				info, err := src.Stat(rel)
				if err != nil {
					return &SourceError{Path: rel, Err: err}
				}
				if total += info.Size; total > MaxPackageBytes {
					return errPackageTooLarge(rel)
				}
			}
			p.files = append(p.files, rel)
		}
		return nil
	}
	if err := walk("."); err != nil {
		return p, err
	}
	// The digest of the contents follows this order, so it must not depend on the
	// order the file system lists a directory in. A directory still comes after its
	// parent, which is what the copy needs.
	slices.Sort(p.dirs)
	slices.Sort(p.files)
	if !slices.Contains(p.files, path.Clean(p.manifest.Entry.Program)) {
		return p, &ChangeError{Reason: ChangeInvalid, Field: "entry.program", Err: fmt.Errorf("the package holds no file %q", p.manifest.Entry.Program)}
	}
	return p, nil
}

// StageAdapter copies the package that src is rooted at into a staging
// directory next to the versions of its adapter, and checks that the manifest of
// the copy is, byte for byte, the one that was read and judged first. Nothing is
// installed: everything it writes is preparation that the repair of the adapter
// directory removes after a crash, and that Unstage removes when the installation
// is refused. A package that changes between the check and the copy is refused,
// so what the install runs is what was checked.
//
// After preflight, the package is copied one entry at a time. Link targets are
// judged during the copy, and a failed copy cleans up what was written to the
// staging directory. A package over MaxPackageBytes is refused. rnd names the
// staging directory. The platform the manifest lists is the caller's to check
// on Staged.Manifest.
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
	digest, err := c.copyPackage(src, p, staged)
	if err != nil {
		return fail(err)
	}
	staged.Digest = digest
	data, err := c.w.Reader().ReadFile(path.Join(staged.Dir, manifest.FileName))
	if err != nil {
		return fail(fmt.Errorf("config: reading the staged manifest: %w", err))
	}
	// The manifest that is parsed and judged is the one that was read first; the
	// copy must hold those bytes exactly, or what the install runs is not what was
	// checked (a manifest that kept its id and version and changed its entry
	// program, its platforms or its declarations is a different package).
	if !bytes.Equal(data, p.raw) {
		return fail(&ChangeError{Reason: ChangeInvalid, Field: manifest.FileName,
			Err: errors.New("the manifest differs between the read of the package and the copy: the package changed while it was copied")})
	}
	staged.Manifest = p.manifest
	return staged, nil
}

// copyPackage copies the package into the staging directory and returns the digest
// of what it copied: SHA-256 over every directory by its path and every file by its
// path, length and bytes, in the order of the sorted lists of the package. A file's
// mode is not part of it: the program's is a consequence of the manifest, which is.
func (c *Config) copyPackage(src fsx.Reader, p pkg, staged Staged) (string, error) {
	r := c.w.Reader()
	sum := sha256.New()
	tx := c.w.Begin(adapterStage)
	defer tx.Close()
	for _, d := range []string{AdapterDir(staged.ID), AdapterVersionsDir(staged.ID)} {
		there, err := fsx.Exists(r, d)
		if err != nil {
			return "", err
		}
		if !there {
			if err := tx.MakeDir(roleParent, d); err != nil {
				return "", err
			}
		}
	}
	if err := tx.MakeDir(roleStage, staged.Dir); err != nil {
		return "", err
	}
	for _, d := range p.dirs {
		if err := tx.MakeDir(roleSubdir, path.Join(staged.Dir, d)); err != nil {
			return "", err
		}
		fmt.Fprintf(sum, "dir\x00%s\x00", d)
	}
	program := path.Clean(p.manifest.Entry.Program)
	var total int64
	for _, f := range p.files {
		data, err := src.ReadFileLimit(f, MaxPackageBytes-total)
		if errors.Is(err, fsx.ErrTooLarge) {
			return "", errPackageTooLarge(f)
		}
		if err != nil {
			return "", &SourceError{Path: f, Err: err}
		}
		total += int64(len(data))
		fmt.Fprintf(sum, "file\x00%s\x00%d\x00", f, len(data))
		sum.Write(data)
		mode := fsx.ModePrivate
		if f == program {
			mode = fsx.ModePrivateExec
		}
		if err := tx.Replace(roleFile, path.Join(staged.Dir, f), data, mode); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// Unstage drops the staging directory of an installation that did not complete,
// and, when the adapter was never installed and nothing else is staged beside it,
// the directory of the adapter that the staging made. It touches nothing else: a
// version directory that was renamed in and did not become current, like one a
// crash left, is the repair's, because
// Unstage runs outside the writer loop and cannot tell such a directory from one
// that an installation committed meanwhile. What is not there is not an error,
// and with nothing to drop it writes nothing.
func (c *Config) Unstage(s Staged) error {
	return c.unstage(s)
}

func (c *Config) unstage(s Staged) error {
	r := c.w.Reader()
	installed, err := fsx.Exists(r, AdapterCurrentPath(s.ID))
	if err != nil {
		return err
	}
	dropStage, err := fsx.Exists(r, s.Dir)
	if err != nil {
		return err
	}
	dropParent := false
	if !installed {
		if dropParent, err = stagedAlone(r, s); err != nil {
			return err
		}
	}
	if !dropStage && !dropParent {
		return nil
	}
	tx := c.w.Begin(adapterUnstage)
	defer tx.Close()
	if dropStage {
		if err := tx.Discard(roleStage, s.Dir); err != nil {
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

// stagedAlone says whether the directory of the adapter exists and holds nothing
// but the staging directory of s (or nothing at all), which is what a first
// installation leaves when it is dropped.
func stagedAlone(r fsx.Reader, s Staged) (bool, error) {
	there, err := fsx.Exists(r, AdapterDir(s.ID))
	if err != nil || !there {
		return false, err
	}
	versions, err := r.ReadDir(AdapterVersionsDir(s.ID))
	switch {
	case missing(err):
		return true, nil
	case err != nil:
		return false, &FileError{Path: AdapterVersionsDir(s.ID), Kind: KindDir, Reason: FileUnreadable, Err: err}
	}
	for _, v := range versions {
		if v.Name != path.Base(s.Dir) {
			return false, nil
		}
	}
	return true, nil
}

// Install is the plan of the writes that make a staged package the current
// version of its adapter. The composing package runs them in its own
// transaction, in this order, with its own write between Point and DiscardOld:
// Promote, Point, DiscardOld. The commit point is the replacement
// of the current pointer. PlanInstall, before the transaction begins, does every
// check and encoding the writes need, so none of them can fail for a reason of
// its own.
type Install struct {
	// AlreadyCurrent is true when the staged package is the one that is current,
	// the same version with the same contents: there is nothing to write, and the
	// installation is already done.
	AlreadyCurrent bool
	// Replaced is true when the package replaces one that was installed: another
	// version, or the same version with other contents. A running process of the
	// adapter runs the package that was replaced until it is started again.
	Replaced bool

	c      *Config
	staged Staged
	dir    string // the directory the staged package is renamed to; no directory of that name exists
	// previousDir is the directory of the package that is replaced, which the
	// installation discards; empty for a first installation, and when a process
	// of the adapter may still hold its files, in which case the repair of the
	// adapter directory removes it at the next start.
	previousDir string
	current     []byte // the pointer file of the staged package, encoded by PlanInstall
	next        AdapterCurrent
}

var errAlreadyCurrent = errors.New("config: the staged package is already current")

// freeRevision is the revision the package of the given version takes when it is
// installed over the one the pointer names (cur, empty for a first installation):
// the first whose directory does not exist, so that an installation never writes
// into or discards a directory it did not make. A directory of that version that
// exists is the package the pointer names, one that was replaced while a process
// may still run from it, or one a failed installation left; the repair removes
// those that the pointer does not name. Revisions of the version that is current
// count on from the current one, so a package that is installed again is never
// given the name of a directory that was discarded a moment ago.
func freeRevision(r fsx.Reader, id, version string, cur AdapterCurrent) (uint32, error) {
	revision := uint32(0)
	if cur.Version == version {
		revision = cur.Revision + 1
	}
	for {
		there, err := fsx.Exists(r, AdapterRevisionDir(id, version, revision))
		if err != nil || !there {
			return revision, err
		}
		revision++
	}
}

// StillEnding says whether a process of the instance is still being ended. Config
// does not run adapters, so the caller that does tells it.
type StillEnding func(instance string) bool

// PlanInstall plans the installation of a staged package.
//
// The same version with the same contents (the digest the staging computed) is
// already installed and writes nothing. Any other package of the adapter, another
// version or the same version with other contents, replaces the installed one under
// a directory of its own, one that does not exist yet, the pointer's replacement
// being what switches to it; the processes of the adapter keep running the files
// they were started from, so no instance is refused, and the caller restarts the
// enabled ones. The directory they run from is not discarded while one may still
// hold it (an enabled instance, or a disabled one whose process is still ending;
// ending says so, and nil says none is): a running executable does not allow that on
// every platform, and the repair of the adapter directory removes it at the next
// start. No other directory of the adapter is discarded either, for the same reason.
func (c *Config) PlanInstall(s Staged, ending StillEnding) (Install, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	st := c.read()
	plan := Install{c: c, staged: s}
	var cur AdapterCurrent
	if installed, ok := st.adapters[s.ID]; ok {
		cur = AdapterCurrent{Version: installed.Version, Digest: installed.Digest, Revision: installed.Revision}
		// A package that could not be loaded is replaced even by one with its own
		// digest: what the pointer names is what is wrong.
		if installed.Fault == nil && cur.Version == s.Version && cur.Digest != "" && cur.Digest == s.Digest {
			plan.AlreadyCurrent = true
			return plan, nil
		}
		plan.Replaced = true
		// The directory of a package that could not be loaded is not known to be
		// this one's, so it is left to the repair as well.
		if installed.Fault == nil && !holdsFiles(st, s.ID, ending) {
			plan.previousDir = installed.Dir()
		}
	}
	revision, err := freeRevision(c.w.Reader(), s.ID, s.Version, cur)
	if err != nil {
		return Install{}, err
	}
	plan.next = AdapterCurrent{Version: s.Version, Digest: s.Digest, Revision: revision}
	plan.dir = AdapterRevisionDir(s.ID, s.Version, revision)
	if plan.current, err = EncodeAdapterCurrent(plan.next); err != nil {
		return Install{}, err
	}
	return plan, nil
}

// holdsFiles says whether a process of an instance of the adapter may hold the files
// of the installed package, which the caller must then leave alone: an enabled
// instance runs them, and a disabled one may still have a process that is ending.
func holdsFiles(st state, adapter string, ending StillEnding) bool {
	for _, name := range sortedKeys(st.instances.Items) {
		in := st.instances.Items[name]
		if in.Adapter != adapter {
			continue
		}
		if in.Enabled || (ending != nil && ending(name)) {
			return true
		}
	}
	return false
}

// Promote renames the staging directory to the version, under RoleVersion.
func (i Install) Promote(tx *txn.Tx) error {
	if i.AlreadyCurrent {
		return errAlreadyCurrent
	}
	return tx.RenameDir(RoleVersion, i.staged.Dir, i.dir)
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
	adapter := Adapter{ID: i.staged.ID, Version: i.next.Version, Digest: i.next.Digest, Revision: i.next.Revision, Manifest: cloneManifest(i.staged.Manifest)}
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

// DiscardOld discards the package that was replaced, under RoleOld. It writes
// nothing for a first installation, and nothing when a process may still hold the
// package's files (see PlanInstall), which the repair removes at the next start.
func (i Install) DiscardOld(tx *txn.Tx) error {
	if i.AlreadyCurrent {
		return errAlreadyCurrent
	}
	if i.previousDir == "" {
		return nil
	}
	return tx.Discard(RoleOld, i.previousDir)
}
