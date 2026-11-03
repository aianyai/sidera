package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aianyai/sidera/cmd/sidera/internal/launch"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/fetch"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/swap"
	"github.com/aianyai/sidera/cmd/sidera/internal/update/verify"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/release"
	"github.com/aianyai/sidera/sdk/go/semver"
)

const (
	checksumsAsset = "checksums.txt"
	signatureAsset = "checksums.txt.minisig"

	programName = "sidera"
	consoleName = "sidera-adapter-console"
	lockName    = "update.lock"
)

// session is one run or check: the configuration, what has been learned of the
// release and the report so far.
type session struct {
	cfg    Config
	rep    Report
	target string
	// installed and wanted are the running program's version and the target, parsed.
	installed, wanted semver.Version
	rel               verify.Release
	// programs are the files a replacement would replace, set by refuseLocation.
	programs []programFile
}

// Check reads the release want (the latest when empty) and says whether it is newer
// than the running program, whether its signed floor allows the update, and the first
// reason a run would stop. It downloads two small files into memory and writes
// nothing, and it does not look at the daemon or the adapters. A release that cannot
// be believed is an error, not a state.
func Check(ctx context.Context, cfg Config, want string) (Verdict, error) {
	s, err := newSession(cfg)
	if err != nil {
		return Verdict{}, err
	}
	if err := s.discover(ctx, want); err != nil {
		return Verdict{Release: s.rep.Release}, err
	}
	if err := s.verifyRelease(ctx); err != nil {
		return Verdict{Release: s.rep.Release}, err
	}
	v := Verdict{Release: s.rep.Release, UpdateAvailable: semver.Compare(s.installed, s.wanted) < 0}
	refusals := []func() error{s.refuseBuild, s.refuseDowngrade, s.refuseFloor}
	if v.UpdateAvailable {
		// Where the programs are matters only when they would be replaced.
		refusals = append(refusals, s.refuseLocation)
	}
	for _, refuse := range refusals {
		if err := refuse(); err != nil {
			v.Refusal = err.Error()
			break
		}
	}
	return v, nil
}

// Run brings the programs, the daemon and the adapters to the release want (the
// latest when empty). The report says what was done even when the run failed.
func Run(ctx context.Context, cfg Config, want string) (Report, error) {
	s, err := newSession(cfg)
	if err != nil {
		return Report{}, err
	}
	err = s.run(ctx, want)
	return s.rep, err
}

func newSession(cfg Config) (*session, error) {
	installed, err := release.ParseVersion(cfg.Installed.Version)
	if err != nil {
		return nil, fmt.Errorf("the running program: %w", err)
	}
	s := &session{cfg: cfg, installed: installed}
	s.rep.Current = cfg.Installed.Version
	return s, nil
}

func (s *session) step(text string) {
	if s.cfg.Step != nil {
		s.cfg.Step(text)
	}
}

func (s *session) run(ctx context.Context, want string) error {
	cfg := s.cfg
	if cfg.Node == nil {
		return errors.New("update: no node to bring up to date")
	}
	ch, err := channelOf(cfg.Installed)
	if err != nil {
		return err
	}
	// Everything up to the lock only reads, so that a run that is refused leaves
	// nothing behind: a refused run under sudo must not leave a cache directory and a
	// lock that belong to root where the next run of the user would meet them.
	if err := s.discover(ctx, want); err != nil {
		return err
	}
	if err := s.refuseDowngrade(); err != nil {
		return err
	}
	needProgram := semver.Compare(s.installed, s.wanted) < 0
	if needProgram {
		if err := s.refuseLocation(); err != nil {
			return err
		}
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	defer s.dropCache()
	if needProgram {
		if err := s.checkWritable(); err != nil {
			return err
		}
	}

	// What the node needs is read before anything is downloaded, so that a run that
	// has nothing to do, or cannot reach the daemon, says so at once.
	node, err := s.readNode(ctx)
	if err != nil {
		return err
	}
	if !needProgram && !node.restart && len(node.stale) == 0 {
		// With no daemon running the adapters were not looked at, and nothing is
		// current that was not seen to be.
		s.rep.AlreadyCurrent = node.running
		s.rep.Adapters = append(s.rep.Adapters, node.skipped...)
		return nil
	}

	if err := s.verifyRelease(ctx); err != nil {
		return err
	}
	if err := s.refuseFloor(); err != nil {
		return err
	}

	var files string
	if needProgram || len(node.stale) > 0 {
		if files, err = ch.obtain(s, ctx); err != nil {
			return err
		}
	}

	plan := s.plan(files, needProgram, node)
	if cfg.Confirm != nil {
		ok, err := cfg.Confirm(plan)
		if err != nil {
			return err
		}
		if !ok {
			return ErrDeclined
		}
	}

	if needProgram {
		if err := s.replacePrograms(files); err != nil {
			return err
		}
	}
	if node.restart {
		if err := s.restartDaemon(ctx); err != nil {
			return err
		}
	}
	return s.installAdapters(ctx, files, node)
}

// discover learns which release is wanted and fills what the report says of it.
func (s *session) discover(ctx context.Context, want string) error {
	if want == "" {
		s.step("finding the latest release")
		latest, err := s.cfg.Fetch.Latest(ctx)
		if err != nil {
			return err
		}
		want = latest
	}
	wanted, err := release.ParseVersion(want)
	if err != nil {
		return fmt.Errorf("the release asked for: %w", err)
	}
	s.target, s.wanted = want, wanted
	s.rep.Target = want
	s.rep.ReleaseURL = s.cfg.Fetch.ReleaseURL(want)
	return nil
}

// verifyRelease downloads checksums.txt and its signature and believes them or not.
func (s *session) verifyRelease(ctx context.Context) error {
	s.step("verifying the signature of release " + s.target)
	var checksums, signature bytes.Buffer
	if err := s.cfg.Fetch.Download(ctx, s.target, checksumsAsset, &checksums, fetch.MaxChecksums); err != nil {
		return err
	}
	if err := s.cfg.Fetch.Download(ctx, s.target, signatureAsset, &signature, fetch.MaxSignature); err != nil {
		return err
	}
	rel, err := verify.Verify(s.cfg.Keys, checksums.Bytes(), signature.Bytes(), s.target)
	if err != nil {
		return fmt.Errorf("release %s cannot be trusted: %w", s.target, err)
	}
	s.rel = rel
	s.rep.Floor = rel.Floor()
	s.rep.Admitted = rel.Admits(s.cfg.Installed.Version) == nil
	return nil
}

// The refusals. Each rule is here once; a check lists the first that applies and a
// run stops at it.

// refuseBuild refuses an installation whose files this program does not know how to
// replace: only the programs of a release archive are.
func (s *session) refuseBuild() error {
	_, err := channelOf(s.cfg.Installed)
	return err
}

func (s *session) refuseDowngrade() error {
	if semver.Compare(s.installed, s.wanted) > 0 {
		return fmt.Errorf("this is %s, newer than %s: update does not go back to an older release, because what a newer version wrote the older one may not read; to go back, restore the programs by hand (see the operations guide) and expect to restore the data directory as well",
			s.cfg.Installed.Version, s.target)
	}
	return nil
}

func (s *session) refuseFloor() error {
	if err := s.rel.Admits(s.cfg.Installed.Version); err != nil {
		return fmt.Errorf("%w; update to an intermediate release first (see the release notes), or install %s by hand", err, s.target)
	}
	return nil
}

// refuseLocation refuses when the programs that would be replaced are not a place a
// replacement can be trusted with; it reads and writes nothing. It also finds the
// programs.
func (s *session) refuseLocation() error {
	programs, err := s.findPrograms()
	s.programs = programs
	return err
}

// nodeState is what the daemon and the adapters need to follow the release.
type nodeState struct {
	running bool
	version string
	// restart is true when the running daemon is older than the target.
	restart bool
	// stale lists the installed adapters that are older than the target.
	stale []Adapter
	// skipped lists the installed adapters that are left as they are, and why: those
	// that are not the release's to replace.
	skipped []AdapterReport
}

// readNode reads the daemon and the installed adapters and sorts them against the
// target. Nothing here goes back to an older version: a daemon newer than the target
// stops the run, and an adapter newer than the target is left as it is. An adapter
// whose version is not a release version is not one of this release's (a third-party
// or self-built one) and is left as it is.
func (s *session) readNode(ctx context.Context) (nodeState, error) {
	var n nodeState
	st, err := s.cfg.Node.Status(ctx)
	if errors.Is(err, ErrNotRunning) {
		return n, nil
	}
	if err != nil {
		return n, fmt.Errorf("read the daemon: %w", err)
	}
	n.running, n.version = true, st.Version
	s.rep.Daemon.Running, s.rep.Daemon.Previous = true, st.Version
	daemon, err := release.ParseVersion(st.Version)
	if err != nil {
		return n, fmt.Errorf("read the daemon: its version is not a release version: %w", err)
	}
	switch c := semver.Compare(daemon, s.wanted); {
	case c > 0:
		return n, fmt.Errorf("the daemon runs %s, newer than %s: update does not restart a daemon into an older release, because what a newer version wrote the older one may not read; run sidera update with the program of the daemon, or stop the daemon first", st.Version, s.target)
	case c < 0:
		n.restart = true
	}
	adapters, err := s.cfg.Node.Adapters(ctx)
	if err != nil {
		return n, fmt.Errorf("list the installed adapters: %w", err)
	}
	for _, a := range adapters {
		v, err := release.ParseVersion(a.Version)
		switch {
		case err != nil:
			n.skipped = append(n.skipped, AdapterReport{ID: a.ID, Previous: a.Version,
				Skipped: "its version is not a release version, so it is not one of this release's adapters"})
		case semver.Compare(v, s.wanted) > 0:
			n.skipped = append(n.skipped, AdapterReport{ID: a.ID, Previous: a.Version,
				Skipped: "it is newer than " + s.target + ", and update does not go back"})
		case semver.Compare(v, s.wanted) < 0:
			n.stale = append(n.stale, a)
		}
	}
	return n, nil
}

func (s *session) plan(files string, needProgram bool, n nodeState) Plan {
	p := Plan{
		Current: s.rep.Current, Target: s.target, ReleaseURL: s.rep.ReleaseURL,
		Daemon: n.running, Restart: n.restart, DaemonVersion: n.version,
	}
	for _, f := range s.programs {
		p.Replace = append(p.Replace, f.path)
	}
	for _, a := range n.stale {
		if packaged(files, a.ID) {
			p.Adapters = append(p.Adapters, a.ID)
		} else {
			p.Missing = append(p.Missing, a.ID)
		}
	}
	return p
}

// packaged says whether the unpacked release has the package of an adapter.
func packaged(files, id string) bool {
	if files == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(files, "adapters", id))
	return err == nil && fi.IsDir()
}

// replacePrograms replaces the programs next to the running one by those of the
// unpacked release, all or none.
func (s *session) replacePrograms(files string) error {
	dir := filepath.Dir(s.cfg.Program)
	dst, err := fsx.OpenOS(dir, s.cfg.Random)
	if err != nil {
		return err
	}
	src, err := fsx.OpenOSReader(files)
	if err != nil {
		return err
	}
	var items []swap.Item
	var targets []string
	for _, f := range s.programs {
		items = append(items, swap.Item{Target: f.name, Source: f.source})
		targets = append(targets, f.name)
	}
	s.step("replacing " + joinNames(targets))
	cleaned, err := swap.Clean(dst, targets)
	s.rep.Program.Restored = absolute(dir, cleaned.Restored)
	s.rep.Program.Left = absolute(dir, cleaned.Left)
	if err != nil {
		return fmt.Errorf("clear what earlier updates left next to the programs: %w", err)
	}
	pending, err := swap.Prepare(dst, src, items)
	if err != nil {
		return err
	}
	release := holdSignals()
	err = pending.Commit()
	interrupted := release()
	if err != nil {
		return explainRollback(dir, err)
	}
	s.rep.Program.Updated = true
	s.rep.Program.Replaced = absolute(dir, targets)
	if interrupted {
		return errors.New("interrupted after the programs were replaced; run sidera update again to restart the daemon and install the adapters")
	}
	return nil
}

// explainRollback names, with absolute paths, where the previous programs are when
// a failed replacement could not put them back.
func explainRollback(dir string, err error) error {
	var rb *swap.RollbackError
	if !errors.As(err, &rb) {
		return err
	}
	msg := fmt.Sprintf("%v; and the swap could not be undone", rb.Cause)
	for _, old := range rb.Held {
		held := filepath.Join(dir, old)
		msg += fmt.Sprintf("; the previous program is in %s: rename it to %s to restore it", held, filepath.Join(dir, trimOld(old)))
	}
	return errors.New(msg)
}

func (s *session) restartDaemon(ctx context.Context) error {
	s.step("restarting the daemon")
	if err := s.cfg.Node.Restart(ctx, s.target); err != nil {
		return fmt.Errorf("restart the daemon: %w; the programs are at %s, restart the daemon by hand and run sidera update again to install the adapters", err, s.target)
	}
	st, err := s.cfg.Node.Status(ctx)
	if err != nil {
		return fmt.Errorf("read the restarted daemon: %w", err)
	}
	s.rep.Daemon.Restarted, s.rep.Daemon.Version = true, st.Version
	if st.Version != s.target {
		return fmt.Errorf("the daemon restarted as %s, not %s: it was started from another program than the one replaced", st.Version, s.target)
	}
	return nil
}

// installAdapters installs the package of each installed adapter that is older than
// the target, and reports those that are left as they are. Every one is tried; the
// failures are returned together.
func (s *session) installAdapters(ctx context.Context, files string, n nodeState) error {
	s.rep.Adapters = append(s.rep.Adapters, n.skipped...)
	var errs []error
	for _, a := range n.stale {
		rep := AdapterReport{ID: a.ID, Previous: a.Version}
		if !packaged(files, a.ID) {
			rep.Skipped = "the release has no package of this adapter"
			s.rep.Adapters = append(s.rep.Adapters, rep)
			continue
		}
		s.step("installing adapter " + a.ID)
		got, err := s.cfg.Node.Install(ctx, filepath.Join(files, "adapters", a.ID))
		if err != nil {
			rep.Error = err.Error()
			errs = append(errs, fmt.Errorf("install adapter %s: %w", a.ID, err))
		} else {
			rep.Version, rep.Replaced = got.Version, got.Replaced
		}
		s.rep.Adapters = append(s.rep.Adapters, rep)
	}
	return errors.Join(errs...)
}

// channel is the step that brings the files of the target to this machine and says
// where they are. Which one applies depends on how the installation was made.
type channel struct {
	obtain func(s *session, ctx context.Context) (files string, err error)
}

// channelOf selects how the files of a release are obtained for a build, or refuses
// the builds that update does not replace.
func channelOf(info release.Info) (channel, error) {
	switch {
	case info.Distribution == release.DistributionArchive && !info.Modified:
		return channel{obtain: (*session).obtainArchive}, nil
	case info.Distribution == release.DistributionArchive:
		return channel{}, errors.New("this program is a release build from a modified checkout; rebuild it from source instead of updating")
	case info.Version == release.DevVersion:
		return channel{}, errors.New("this program is a development build; rebuild it from source instead of updating")
	default:
		return channel{}, errors.New("this program was not installed from a release archive, so sidera update cannot replace it; update it the way it was installed")
	}
}

// launchSmoke runs a program as `sidera version --json`.
func launchSmoke(ctx context.Context, program string) (release.Info, error) {
	return launch.Launch{Program: program}.Probe(ctx)
}
