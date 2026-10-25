package config_test

import (
	"bytes"
	"testing"

	"github.com/aianyai/sidera/internal/admin"
	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
)

// On a clean run, with no crash and no damage, there is no recovery work for
// config at any quiet point: the garbage predicate of the adapter repair, the
// one the repair itself uses, finds nothing, and neither does the cleanup of
// temporary names. The steps cover every lifecycle event of config: loading,
// each management change, an installation that succeeds, one that is refused
// because the adapter is in use, a first installation that is refused, a
// package that cannot be staged, a reinstallation of the version that is
// current (planned as already current, then unstaged), and minting stable ids.
func TestCleanScenariosLeaveNoConfigRecoveryWork(t *testing.T) {
	rg, c := loaded(t)
	rnd := bytes.NewReader(bytes.Repeat([]byte{0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78}, 8))
	open := isOpen(t)
	version := func() (n, p, i uint64) {
		return uint64(c.Node().Version), uint64(c.Presets().Version), uint64(c.Instances().Version)
	}
	quiet := func(step string) {
		t.Helper()
		if work, err := config.AdapterGCWork(rg.mem); err != nil || len(work) != 0 {
			t.Errorf("after %s: adapter gc work = %+v, %v", step, work, err)
		}
		if work := mustCleanup(t, rg.mem); len(work) != 0 {
			t.Errorf("after %s: cleanup work = %v", step, work)
		}
	}
	quiet("loading a clean tree")

	steps := []struct {
		name string
		do   func() error
	}{
		{"a node setting", func() error {
			n, _, _ := version()
			_, err := c.SetNodeSettings(request(1, admin.Version(n)), []byte(`{"reply_permission": false}`), result(new([]config.Node), `{}`))
			return err
		}},
		{"adding a preset", func() error {
			_, p, _ := version()
			_, err := c.AddPreset(request(2, admin.Version(p)), "scratch", []byte(`{"local_sources": "deny"}`), result(new([]config.Presets), `{}`))
			return err
		}},
		{"removing a preset", func() error {
			_, p, _ := version()
			_, err := c.RemovePreset(request(3, admin.Version(p)), "scratch", result(new([]config.Presets), `{}`))
			return err
		}},
		{"enabling a second instance", func() error {
			_, _, i := version()
			_, err := c.EnableAdapter(request(4, admin.Version(i)), "second", "example", nil, open, result(new([]config.Instances), `{}`))
			return err
		}},
		{"an endpoint setting", func() error {
			_, _, i := version()
			_, err := c.SetEndpointSettings(request(5, admin.Version(i)), "second", mustEndpoint(t, ep4Text), []byte(`{"backlog_limit": 3}`), open, result(new([]config.Instances), `{}`))
			return err
		}},
		{"minting a direct endpoint id", func() error {
			ch, err := c.DirectID("second", "k", rnd)
			if err != nil {
				return err
			}
			tx := rg.w.Begin(testDirectRegister)
			defer tx.Close()
			done, err := c.PutEndpointIDs(tx, ch)
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			done()
			return nil
		}},
		{"an installation refused for an adapter in use", func() error {
			staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
			if err != nil {
				return err
			}
			if _, err := c.PlanInstall(staged); err == nil {
				t.Error("an adapter with enabled instances was planned for an upgrade")
			}
			return c.Unstage(staged)
		}},
		{"a first installation refused after staging", func() error {
			staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
			if err != nil {
				return err
			}
			return c.Unstage(staged)
		}},
		{"a package that cannot be staged", func() error {
			if _, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", map[string]string{"lib/.tmp-x": "x"}), suffixReader()); err == nil {
				t.Error("a package with a reserved name was staged")
			}
			return nil
		}},
		{"disabling both instances", func() error {
			_, _, i := version()
			if _, err := c.DisableAdapter(request(6, admin.Version(i)), "example", open, result(new([]config.Instances), `{}`)); err != nil {
				return err
			}
			_, err := c.DisableAdapter(request(7, admin.Version(i+1)), "second", open, result(new([]config.Instances), `{}`))
			return err
		}},
		{"an upgrade", func() error {
			staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
			if err != nil {
				return err
			}
			return install(t, rg, c, staged)
		}},
		{"a first installation", func() error {
			staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
			if err != nil {
				return err
			}
			return install(t, rg, c, staged)
		}},
		{"reinstalling the version that is current", func() error {
			staged, err := c.StageAdapter(srcPackage(t, "fresh", "0.1.0", nil), suffixReader())
			if err != nil {
				return err
			}
			plan, err := c.PlanInstall(staged)
			if err != nil {
				return err
			}
			if !plan.AlreadyCurrent {
				t.Error("the version that is current was planned for installation again")
			}
			// Nothing is installed, so the caller drops the staging directory.
			return c.Unstage(staged)
		}},
		{"confirming the backup again", func() error {
			b, _ := c.Backup()
			pb, err := config.PrepareBackup(b)
			if err != nil {
				return err
			}
			tx := rg.w.Begin(testBackupConfirm)
			defer tx.Close()
			done, err := c.PutBackup(tx, pb)
			if err != nil {
				return err
			}
			if err := tx.Replace("result", "state/r", []byte("x"), fsx.ModePrivate); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			done()
			return nil
		}},
	}
	ensureState(t, rg.mem)
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		quiet(s.name)
	}
	// And a restart on the disk the whole run left finds nothing to repair: a
	// start with no repair work reaches no point of the repair.
	w := newWalker(rg.mem, 1, func(where string, _ *fsxtest.Mem, _ bool) { t.Errorf("a start on a clean disk reached %s", where) })
	onDisk(t, rg.mem, w).load()
}
