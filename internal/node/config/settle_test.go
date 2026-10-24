package config_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/aianyai/sidera/internal/node/config"
	"github.com/aianyai/sidera/internal/node/fsx"
	"github.com/aianyai/sidera/internal/node/fsx/fsxtest"
	"github.com/aianyai/sidera/internal/node/txn"
)

// installedVersion reads the version the current pointer of an adapter names,
// and fails the test when that version has no directory: an installed adapter
// whose files are gone.
func installedVersion(t *testing.T, where string, disk *fsxtest.Mem, id string) string {
	t.Helper()
	data, err := disk.ReadFile(config.AdapterCurrentPath(id))
	if err != nil {
		t.Errorf("%s: the current pointer of %s: %v", where, id, err)
		return ""
	}
	version, err := config.DecodeAdapterCurrent(id, data)
	if err != nil {
		t.Errorf("%s: the current pointer of %s: %v", where, id, err)
		return ""
	}
	if !exists(disk, config.AdapterManifestPath(id, version)) {
		t.Errorf("%s: current names %s %s, whose directory is gone", where, id, version)
	}
	return version
}

// An upgrade crashes after the rename of the current pointer and before the
// sync of its directory: the new pointer is visible and not durable. The next
// start repairs the adapter directory and discards the old version, durably,
// because the pointer no longer names it. A power loss after that must not
// bring the old pointer back to a version that is gone: the start has to make
// what it read durable before it acts on it.
func TestPowerLossCloneAdapterGCAfterAnInstallCrashedBeforeTheDirectorySync(t *testing.T) {
	mem := treeDisk(t)
	doc := strings.Replace(goldenInstances, `"enabled": true`, `"enabled": false`, 1)
	if err := fsx.Replace(mem, setupTag, config.InstancesPath, []byte(doc), fsx.ModePrivate); err != nil {
		t.Fatal(err)
	}

	const crashPoint = "adapter-install/current.sync-dir#1"
	var crashed *fsxtest.Mem
	obs := pointFunc(func(p fsx.Point) {
		if p.String() == crashPoint {
			crashed = mem.Clone()
		}
	})
	rg := onDisk(t, mem, obs)
	c := rg.load()
	staged, err := c.StageAdapter(srcPackage(t, "example", "2.0.0", nil), suffixReader())
	if err != nil {
		t.Fatal(err)
	}
	if err := install(t, rg, c, staged); err != nil {
		t.Fatalf("install: %v", err)
	}
	if crashed == nil {
		t.Fatalf("%s was not reached", crashPoint)
	}
	// The crash is a real hazard: the new pointer is visible, and a power loss
	// right then would take it back.
	if got := installedVersion(t, "at the crash", crashed, "example"); got != "2.0.0" {
		t.Fatalf("the pointer at the crash names %q, want 2.0.0", got)
	}
	if got := installedVersion(t, "at the crash, worst power loss", crashed.CrashCloneWorst(), "example"); got != "1.0.0" {
		t.Fatalf("the test shows nothing: a power loss at the crash leaves the pointer at %q, want 1.0.0", got)
	}

	// The next start: Recover, then Load, which repairs the adapter directory.
	w := txn.New(crashed, newTable(t), nil)
	if err := w.Recover(); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if _, err := config.Load(w); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if exists(crashed, config.AdapterVersionDir("example", "1.0.0")) {
		t.Fatal("the repair did not discard the old version: the test shows nothing")
	}

	losses := map[string]*fsxtest.Mem{"worst": crashed.CrashCloneWorst()}
	for seed := 1; seed <= 16; seed++ {
		losses[fmt.Sprintf("seed-%d", seed)] = crashed.CrashClone(rand.New(rand.NewPCG(uint64(seed), 0xada97e6c)))
	}
	for name, lost := range losses {
		where := "power loss " + name + " after the repair"
		if got := installedVersion(t, where, lost, "example"); got != "2.0.0" {
			t.Errorf("%s: current names %q, want 2.0.0", where, got)
		}
		if _, err := restart(t, lost); err != nil {
			t.Errorf("%s: the next start refuses the tree: %v", where, err)
		}
	}
}

// pointFunc is an Observer that calls a function for every point.
type pointFunc func(p fsx.Point)

func (f pointFunc) Reached(p fsx.Point, _ txn.Kind) { f(p) }
